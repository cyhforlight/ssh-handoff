//go:build linux || darwin

package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
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

func TestShellCommandContextBoundsInheritedOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = readyRead.Close()
	}()

	cmd := shellCommandContext(
		ctx,
		`/bin/sh -c 'trap "" HUP; sleep 2 & printf x >&3; wait'`,
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.ExtraFiles = []*os.File{readyWrite}

	if err := cmd.Start(); err != nil {
		_ = readyWrite.Close()
		t.Fatal(err)
	}
	if err := readyWrite.Close(); err != nil {
		t.Fatal(err)
	}

	var ready [1]byte
	if _, err := io.ReadFull(readyRead, ready[:]); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	cancel()
	err = cmd.Wait()

	if err == nil {
		t.Fatal("Wait unexpectedly succeeded after cancellation")
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("Wait took %s; inherited output pipe was not bounded", elapsed)
	}
}
