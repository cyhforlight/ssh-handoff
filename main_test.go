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

func TestParseOpenArguments(t *testing.T) {
	check := func(arguments []string, want openOptions) {
		t.Helper()
		got, err := parseOpenArguments(arguments)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("parseOpenArguments() = %#v, want %#v", got, want)
		}
	}
	check([]string{
		"--host", "2001:db8::10", "--user", "operator",
		"--identity", "/keys/work key", "--mode", "shell-pty",
		"--note", "production",
	}, openOptions{
		Connection: connectionSpec{
			Host:     "2001:db8::10",
			User:     "operator",
			Port:     22,
			Identity: "/keys/work key",
		},
		Note: "production",
		Mode: modeShellPTY,
	})
	check([]string{"--profile", "myserver"}, openOptions{
		Connection: connectionSpec{Profile: "myserver"},
		Mode:       modeExec,
	})
}

func TestParseOpenArgumentsRejectsInvalidConnections(t *testing.T) {
	for _, arguments := range [][]string{
		{"--host", "example.com"},
		{"--user", "operator"},
		{"--host", "example.com", "--user", "operator", "--port", "0"},
		{"--host", "example.com", "--user", "operator", "--port", "65536"},
		{"--profile", "myserver", "--host", "example.com"},
		{"--profile", "myserver", "--port", "22"},
		{"--profile", "myserver", "--identity", ""},
		{"--profile", ""},
		{"--host", "-oProxyCommand=proxy", "--user", "operator"},
		{"ssh", "operator@example.com"},
	} {
		if _, err := parseOpenArguments(arguments); err == nil {
			t.Errorf("parseOpenArguments(%q) unexpectedly succeeded", arguments)
		}
	}
}

func TestNormalizeStdinCommandUsesPOSIXLineEndings(t *testing.T) {
	input := []byte("first\r\nsecond\nthird\rfourth\r\n")
	want := "first\nsecond\nthird\rfourth\n"
	if got := normalizeStdinCommand(input); got != want {
		t.Fatalf("normalizeStdinCommand() = %q, want %q", got, want)
	}
}
