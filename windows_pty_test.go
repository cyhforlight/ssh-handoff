//go:build windows

package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestWindowsPTYRunsConsoleProcess(t *testing.T) {
	path := os.Getenv("ComSpec")
	if path == "" {
		path = `C:\Windows\System32\cmd.exe`
	}
	terminal, err := startWindowsPTY(
		path,
		[]string{"/d", "/c", "echo __SSH_HANDOFF_CONPTY__"},
		80,
		24,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	if err := terminal.Resize(100, 30); err != nil {
		t.Fatalf("resize ConPTY: %v", err)
	}

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
	if got := string(<-output); !strings.Contains(got, "__SSH_HANDOFF_CONPTY__") {
		t.Fatalf("ConPTY output is missing marker: %q", got)
	}
}

func TestWindowsPTYTerminatesProcessJob(t *testing.T) {
	path := os.Getenv("ComSpec")
	if path == "" {
		path = `C:\Windows\System32\cmd.exe`
	}
	terminal, err := startWindowsPTY(
		path,
		[]string{"/d", "/c", "ping -n 30 127.0.0.1 >nul"},
		80,
		24,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()

	outputDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, terminal.Output)
		close(outputDone)
	}()
	terminal.Terminate()
	exitCode, err := terminal.Wait()
	if err != nil {
		t.Fatal(err)
	}
	<-outputDone
	if exitCode == 0 {
		t.Fatal("terminated ConPTY process reported success")
	}
}
