package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelp(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "root flag", args: []string{"--help"}, want: "ssh-handoff open"},
		{name: "subcommand long flag", args: []string{"run", "--help"}, want: runUsage},
		{name: "subcommand flag", args: []string{"open", "-h"}, want: openUsage},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runCLI(test.args, nil, &stdout, &stderr); code != 0 {
				t.Fatalf("runCLI() code = %d, stderr = %s", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), test.want) {
				t.Fatalf("help output does not contain %q: %s", test.want, stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("help writes stderr: %s", stderr.String())
			}
		})
	}
}

func TestRunStreamWritesNDJSONError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runCLI([]string{"run", "--stream"}, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("runCLI() code = %d, want 2", code)
	}
	want := "{\"type\":\"error\",\"error\":{\"code\":\"invalid_arguments\",\"message\":\"usage: " +
		runUsage + "\"}}\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("run writes stderr: %s", stderr.String())
	}
}

func TestNormalizeStdinCommandUsesPOSIXLineEndings(t *testing.T) {
	input := []byte("first\r\nsecond\nthird\rfourth\r\n")
	want := "first\nsecond\nthird\rfourth\n"
	if got := normalizeStdinCommand(input); got != want {
		t.Fatalf("normalizeStdinCommand() = %q, want %q", got, want)
	}
}
