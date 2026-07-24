package main

import (
	"bytes"
	"testing"
)

func TestStreamingRunOutput(t *testing.T) {
	t.Run("exec", func(t *testing.T) {
		var buffer bytes.Buffer
		output := newRunOutput(&buffer, true)
		writer := newSerializedOutput(output.emit)

		stdout := writer.writer(streamStdout)
		stderr := writer.writer(streamStderr)
		_, _ = stdout.Write([]byte{0xe4, 0xb8})
		if buffer.Len() != 0 {
			t.Fatalf("incomplete UTF-8 was emitted: %q", buffer.String())
		}
		_, _ = stdout.Write([]byte{0xad, '\n'})
		if got := buffer.String(); got != "{\"type\":\"stdout\",\"data\":\"中\\n\"}\n" {
			t.Fatalf("stdout was not emitted before completion: %q", got)
		}
		_, _ = stderr.Write([]byte("warning\n"))

		exitCode := 7
		if err := output.writeResult(runStatus{
			Session:  "A3B4",
			Mode:     modeExec,
			ExitCode: &exitCode,
		}); err != nil {
			t.Fatal(err)
		}
		want := "" +
			"{\"type\":\"stdout\",\"data\":\"中\\n\"}\n" +
			"{\"type\":\"stderr\",\"data\":\"warning\\n\"}\n" +
			"{\"type\":\"result\",\"session\":\"A3B4\",\"mode\":\"exec\",\"exit_code\":7,\"timed_out\":false}\n"
		if got := buffer.String(); got != want {
			t.Fatalf("stream output = %q, want %q", got, want)
		}
	})

	t.Run("shell-pty", func(t *testing.T) {
		var buffer bytes.Buffer
		output := newRunOutput(&buffer, true)
		if err := output.emit(streamOutput, []byte("prompt\r\n")); err != nil {
			t.Fatal(err)
		}
		exitCode := 0
		if err := output.writeResult(runStatus{
			Session:  "C5D6",
			Mode:     modeShellPTY,
			ExitCode: &exitCode,
		}); err != nil {
			t.Fatal(err)
		}
		want := "" +
			"{\"type\":\"output\",\"data\":\"prompt\\r\\n\"}\n" +
			"{\"type\":\"result\",\"session\":\"C5D6\",\"mode\":\"shell-pty\",\"exit_code\":0,\"timed_out\":false}\n"
		if got := buffer.String(); got != want {
			t.Fatalf("stream output = %q, want %q", got, want)
		}
	})
}

func TestBufferedRunOutputKeepsResultShape(t *testing.T) {
	var buffer bytes.Buffer
	output := newRunOutput(&buffer, false)
	if err := output.emit(streamStdout, []byte("server\n")); err != nil {
		t.Fatal(err)
	}
	if err := output.emit(streamStderr, []byte("warning\n")); err != nil {
		t.Fatal(err)
	}
	exitCode := 0
	if err := output.writeResult(runStatus{
		Session:  "A3B4",
		Mode:     modeExec,
		ExitCode: &exitCode,
	}); err != nil {
		t.Fatal(err)
	}
	want := "{\n" +
		"  \"session\": \"A3B4\",\n" +
		"  \"mode\": \"exec\",\n" +
		"  \"stdout\": \"server\\n\",\n" +
		"  \"stderr\": \"warning\\n\",\n" +
		"  \"exit_code\": 0,\n" +
		"  \"timed_out\": false\n" +
		"}\n"
	if got := buffer.String(); got != want {
		t.Fatalf("buffered output = %q, want %q", got, want)
	}
}
