package main

import (
	"bytes"
	"errors"
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

		if err := output.writeResult(runStatus{
			Session:  "A3B4",
			Mode:     modeExec,
			ExitCode: new(7),
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
		if err := output.writeResult(runStatus{
			Session:  "C5D6",
			Mode:     modeShellPTY,
			ExitCode: new(0),
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
	if err := output.writeResult(runStatus{
		Session:  "A3B4",
		Mode:     modeExec,
		ExitCode: new(0),
	}); err != nil {
		t.Fatal(err)
	}
	want := "{\n" +
		"  \"session\": \"A3B4\",\n" +
		"  \"mode\": \"exec\",\n" +
		"  \"exit_code\": 0,\n" +
		"  \"timed_out\": false,\n" +
		"  \"stdout\": \"server\\n\",\n" +
		"  \"stderr\": \"\"\n" +
		"}\n"
	if got := buffer.String(); got != want {
		t.Fatalf("buffered output = %q, want %q", got, want)
	}
}

func TestBufferedRunOutputDescribesTimeout(t *testing.T) {
	var buffer bytes.Buffer
	output := newRunOutput(&buffer, false)
	if err := output.writeResult(runStatus{
		Session:  "A3B4",
		Mode:     modeExec,
		TimedOut: true,
		Warning:  timeoutWarning,
	}); err != nil {
		t.Fatal(err)
	}
	want := "{\n" +
		"  \"session\": \"A3B4\",\n" +
		"  \"mode\": \"exec\",\n" +
		"  \"exit_code\": null,\n" +
		"  \"timed_out\": true,\n" +
		"  \"warning\": \"" + timeoutWarning + "\",\n" +
		"  \"stdout\": \"\",\n" +
		"  \"stderr\": \"\"\n" +
		"}\n"
	if got := buffer.String(); got != want {
		t.Fatalf("buffered timeout output = %q, want %q", got, want)
	}
}

func TestBufferedRunOutputReportsWriteFailure(t *testing.T) {
	writeErr := errors.New("write failed")
	output := newRunOutput(errorWriter{err: writeErr}, false)
	if err := output.writeResult(runStatus{}); !errors.Is(err, writeErr) {
		t.Fatalf("writeResult() error = %v, want %v", err, writeErr)
	}
}

type errorWriter struct {
	err error
}

func (writer errorWriter) Write([]byte) (int, error) {
	return 0, writer.err
}
