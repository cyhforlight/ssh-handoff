//go:build linux || darwin

package main

import (
	"strings"
	"testing"
)

func TestInjectSSHPreservesOriginalSuffix(t *testing.T) {
	command := "ssh\t-p 2222 user@example.com  "
	got, err := injectSSH(command, "-S", "/tmp/control socket", "it's-safe")
	if err != nil {
		t.Fatal(err)
	}
	want := "ssh '-S' '/tmp/control socket' 'it'\"'\"'s-safe'\t-p 2222 user@example.com  "
	if got != want {
		t.Fatalf("injectSSH() = %q, want %q", got, want)
	}
}

func TestInjectSSHRejectsInvalidCommand(t *testing.T) {
	for _, command := range []string{
		"ssh",
		"ssh   ",
		"ssh\nhost",
		" ssh host",
		"sshpass host",
	} {
		if _, err := injectSSH(command, "-M"); err == nil {
			t.Errorf("injectSSH(%q) unexpectedly succeeded", command)
		}
	}
}

func TestDownstreamCommandSelectsExecutionMode(t *testing.T) {
	session := &session{
		sessionInfo: sessionInfo{Mode: modeExec, Command: "ssh user@example.com"},
		Platform:    platformSessionState{ControlPath: "/tmp/handoff.sock"},
	}

	execCommand, err := downstreamCommand(session)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(execCommand, "'-tt'") {
		t.Fatalf("exec command unexpectedly requests a PTY: %s", execCommand)
	}
	for _, required := range []string{"'/tmp/handoff.sock'", "'ControlMaster=no'", "'BatchMode=yes'", "'ProxyCommand=false'"} {
		if !strings.Contains(execCommand, required) {
			t.Errorf("exec command is missing %s: %s", required, execCommand)
		}
	}

	session.Mode = modeShellPTY
	ptyCommand, err := downstreamCommand(session)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ptyCommand, "'-tt'") {
		t.Fatalf("shell-pty command does not request a PTY: %s", ptyCommand)
	}
}
