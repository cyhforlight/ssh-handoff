//go:build windows

package main

import (
	"cmp"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsPTYRunsConsoleProcessInUTF8(t *testing.T) {
	path := cmp.Or(os.Getenv("ComSpec"), `C:\Windows\System32\cmd.exe`)
	terminal, err := startWindowsPTY(
		path,
		[]string{"/d", "/c", "chcp & echo __SSH_HANDOFF_CONPTY__"},
		80,
		24,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()

	output := make(chan []byte, 1)
	failures := make(chan error, 1)
	go func() {
		data, readErr := io.ReadAll(terminal.Output)
		output <- data
		failures <- readErr
	}()

	exitCode, err := terminal.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("console process exit code = %d, want 0", exitCode)
	}
	if err := <-failures; err != nil {
		t.Fatal(err)
	}
	got := string(<-output)
	if !strings.Contains(got, "65001") || !strings.Contains(got, "__SSH_HANDOFF_CONPTY__") {
		t.Fatalf("ConPTY output does not confirm UTF-8 execution: %q", got)
	}
}

func TestWindowsPTYStartFailure(t *testing.T) {
	command := cmp.Or(os.Getenv("ComSpec"), `C:\Windows\System32\cmd.exe`)
	missing := filepath.Join(t.TempDir(), "missing.exe")
	for _, target := range []string{"bootstrap", "command"} {
		t.Run(target, func(t *testing.T) {
			path := command
			if target == "bootstrap" {
				t.Setenv("ComSpec", missing)
			} else {
				path = missing
			}
			terminal, err := startWindowsPTY(path, nil, 80, 24)
			if terminal != nil {
				terminal.Terminate()
				terminal.Close()
				t.Fatal("failed startup returned a terminal")
			}
			if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
				t.Fatalf("startup error = %v, want missing executable", err)
			}
		})
	}
}

func TestWindowsPTYTerminatesRunningProcess(t *testing.T) {
	path := cmp.Or(os.Getenv("ComSpec"), `C:\Windows\System32\cmd.exe`)
	terminal, err := startWindowsPTY(path, []string{"/d", "/q", "/c", "set /p SSH_HANDOFF_WAIT="}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	defer terminal.Terminate()

	outputDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, terminal.Output)
		outputDone <- err
	}()

	if !processAlive(terminal.PID()) {
		t.Fatal("console process exited before termination")
	}
	terminal.Terminate()
	exitCode, err := terminal.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if exitCode == 0 || processAlive(terminal.PID()) {
		t.Fatalf("terminated process is still running or exited successfully: %d", exitCode)
	}
	if err := <-outputDone; err != nil {
		t.Fatal(err)
	}
}
