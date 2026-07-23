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
