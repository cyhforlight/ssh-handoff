//go:build windows

package main

import (
	"os"
	"testing"
)

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

	// A pipe is not a console: the helper must treat this as a no-op instead
	// of reporting an error, matching redirected-stdout behavior.
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
