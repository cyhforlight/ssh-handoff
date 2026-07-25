package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sync"
	"unicode/utf8"
)

type outputStream string

const (
	streamStdout outputStream = "stdout"
	streamStderr outputStream = "stderr"
	streamOutput outputStream = "output"
)

type outputSink func(outputStream, []byte) error

type runStatus struct {
	Session  string        `json:"session"`
	Mode     executionMode `json:"mode"`
	ExitCode *int          `json:"exit_code"`
	TimedOut bool          `json:"timed_out"`
}

type commandError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	Error commandError `json:"error"`
}

type runResult struct {
	runStatus
	Stdout *string `json:"stdout,omitzero"`
	Stderr *string `json:"stderr,omitzero"`
	Output *string `json:"output,omitzero"`
}

type runOutput interface {
	emit(outputStream, []byte) error
	writeResult(runStatus) error
	writeError(string, error) error
}

func newRunOutput(writer io.Writer, streaming bool) runOutput {
	if streaming {
		return newNDJSONOutput(writer)
	}
	return &bufferedOutput{writer: writer}
}

type bufferedOutput struct {
	writer io.Writer
	stdout bytes.Buffer
	stderr bytes.Buffer
	output bytes.Buffer
}

func (output *bufferedOutput) emit(stream outputStream, data []byte) error {
	switch stream {
	case streamStdout:
		_, _ = output.stdout.Write(data)
	case streamStderr:
		_, _ = output.stderr.Write(data)
	case streamOutput:
		_, _ = output.output.Write(data)
	default:
		return fmt.Errorf("unknown output stream %q", stream)
	}
	return nil
}

func (output *bufferedOutput) writeResult(status runStatus) error {
	return writeJSON(output.writer, output.result(status))
}

func (output *bufferedOutput) writeError(code string, cause error) error {
	response := commandError{Code: code, Message: cause.Error()}
	return writeJSON(output.writer, errorResponse{Error: response})
}

func (output *bufferedOutput) result(status runStatus) runResult {
	result := runResult{runStatus: status}
	if status.Mode == modeExec {
		result.Stdout, result.Stderr = new(output.stdout.String()), new(output.stderr.String())
	} else {
		result.Output = new(output.output.String())
	}
	return result
}

type outputEvent struct {
	Type outputStream `json:"type"`
	Data string       `json:"data"`
}

type resultEvent struct {
	Type string `json:"type"`
	runStatus
}

type errorEvent struct {
	Type  string       `json:"type"`
	Error commandError `json:"error"`
}

type ndjsonOutput struct {
	encoder *json.Encoder
	carry   map[outputStream][]byte
}

func newNDJSONOutput(writer io.Writer) *ndjsonOutput {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return &ndjsonOutput{
		encoder: encoder,
		carry:   make(map[outputStream][]byte),
	}
}

func (output *ndjsonOutput) emit(stream outputStream, data []byte) error {
	combined := slices.Concat(output.carry[stream], data)

	complete := completeUTF8Prefix(combined)
	output.carry[stream] = bytes.Clone(combined[complete:])
	if complete == 0 {
		return nil
	}
	return output.encoder.Encode(outputEvent{Type: stream, Data: string(combined[:complete])})
}

func (output *ndjsonOutput) writeResult(status runStatus) error {
	if err := output.flush(); err != nil {
		return err
	}
	return output.encoder.Encode(resultEvent{
		Type:      "result",
		runStatus: status,
	})
}

func (output *ndjsonOutput) writeError(code string, cause error) error {
	response := commandError{Code: code, Message: cause.Error()}
	if err := output.flush(); err != nil {
		return err
	}
	return output.encoder.Encode(errorEvent{
		Type:  "error",
		Error: response,
	})
}

func (output *ndjsonOutput) flush() error {
	for _, stream := range []outputStream{streamStdout, streamStderr, streamOutput} {
		if len(output.carry[stream]) == 0 {
			continue
		}
		if err := output.encoder.Encode(outputEvent{
			Type: stream,
			Data: string(output.carry[stream]),
		}); err != nil {
			return err
		}
		delete(output.carry, stream)
	}
	return nil
}

func completeUTF8Prefix(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	start := len(data) - 1
	for start > 0 && !utf8.RuneStart(data[start]) {
		start--
	}
	if utf8.FullRune(data[start:]) {
		return len(data)
	}
	return start
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

type serializedOutput struct {
	mu   sync.Mutex
	sink outputSink
	err  error
}

func newSerializedOutput(sink outputSink) *serializedOutput {
	return &serializedOutput{sink: sink}
}

func (output *serializedOutput) writer(stream outputStream) io.Writer {
	return &streamWriter{output: output, stream: stream}
}

func (output *serializedOutput) failure() error {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.err
}

type streamWriter struct {
	output *serializedOutput
	stream outputStream
}

func (writer *streamWriter) Write(data []byte) (int, error) {
	writer.output.mu.Lock()
	defer writer.output.mu.Unlock()

	if writer.output.err != nil {
		return 0, writer.output.err
	}
	if err := writer.output.sink(writer.stream, data); err != nil {
		writer.output.err = err
		return 0, err
	}
	return len(data), nil
}
