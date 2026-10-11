//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestEnableVirtualTerminalOutput(t *testing.T) {
	const childEnvironment = "SSH_HANDOFF_TEST_CONSOLE"
	if os.Getenv(childEnvironment) != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		// CI may have no console. Give the child its own hidden console so the
		// test also leaves the developer's terminal mode and screen untouched.
		cmd := exec.Command(executable, "-test.run=^"+t.Name()+"$", "-test.v")
		cmd.Env = append(os.Environ(), childEnvironment+"=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{
			CreationFlags: windows.CREATE_NEW_CONSOLE,
			HideWindow:    true,
		}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("console subprocess: %v\n%s", err, output)
		}
		return
	}

	output, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := output.Close(); err != nil {
			t.Error(err)
		}
	}()
	console := windows.Handle(output.Fd())
	const baseMode = windows.ENABLE_PROCESSED_OUTPUT | windows.ENABLE_WRAP_AT_EOL_OUTPUT
	for _, test := range []struct {
		name string
		mode uint32
	}{
		{"enable", baseMode},
		{"already_enabled", baseMode | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := windows.SetConsoleMode(console, test.mode); err != nil {
				t.Fatal(err)
			}
			restore, err := enableVirtualTerminalOutput(output)
			if err != nil {
				t.Fatal(err)
			}
			var mode uint32
			if err := windows.GetConsoleMode(console, &mode); err != nil {
				t.Fatal(err)
			}
			if want := test.mode | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING; mode != want {
				t.Fatalf("enabled mode = %#x, want %#x", mode, want)
			}

			if err := windows.SetConsoleCursorPosition(console, windows.Coord{}); err != nil {
				t.Fatal(err)
			}
			if _, err := output.WriteString("\x1b[3;5HOK"); err != nil {
				t.Fatal(err)
			}
			var info windows.ConsoleScreenBufferInfo
			if err := windows.GetConsoleScreenBufferInfo(console, &info); err != nil {
				t.Fatal(err)
			}
			if want := (windows.Coord{X: 6, Y: 2}); info.CursorPosition != want {
				t.Errorf("cursor after VT output = %+v, want %+v", info.CursorPosition, want)
			}

			restore()
			if err := windows.GetConsoleMode(console, &mode); err != nil {
				t.Fatal(err)
			}
			if mode != test.mode {
				t.Errorf("restored mode = %#x, want %#x", mode, test.mode)
			}
		})
	}
}

func TestEnableVirtualTerminalOutputWithoutConsole(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if closeErr := writer.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()

	// A pipe passed as the output handle must be a no-op.
	restore, err := enableVirtualTerminalOutput(writer)
	if err != nil {
		t.Fatalf("enableVirtualTerminalOutput(pipe) error = %v, want nil", err)
	}
	restore()

	restore, err = enableVirtualTerminalOutput(nil)
	if err != nil {
		t.Fatalf("enableVirtualTerminalOutput(nil) error = %v, want nil", err)
	}
	restore()
}
