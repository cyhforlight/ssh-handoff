//go:build linux || darwin

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestSSHArguments(t *testing.T) {
	assertArgs := func(name string, got, want []string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Fatalf("%s = %#v, want %#v", name, got, want)
		}
	}
	session := &session{
		sessionInfo: sessionInfo{
			Connection: connectionSpec{
				Host:     "2001:db8::10",
				User:     "operator",
				Port:     2222,
				Identity: "/keys/work key",
			},
			Mode: modeExec,
		},
		Platform: platformSessionState{ControlPath: "/tmp/control socket"},
	}

	wantMaster := []string{
		"-M", "-S", "/tmp/control socket", "-tt",
		"-p", "2222", "-l", "operator", "-i", "/keys/work key",
		"--", "2001:db8::10",
	}
	assertArgs("sshMasterArgs()", sshMasterArgs(session), wantMaster)

	wantExec := []string{
		"-S", "/tmp/control socket",
		"-o", "ControlMaster=no",
		"-o", "BatchMode=yes",
		"-o", "ProxyCommand=false",
		"-p", "2222", "-l", "operator", "-i", "/keys/work key",
		"--", "2001:db8::10",
	}
	assertArgs("sshDownstreamArgs()", sshDownstreamArgs(session), wantExec)

	session.Mode = modeShellPTY
	wantPTY := slices.Insert(slices.Clone(wantExec), 8, "-tt")
	assertArgs("sshDownstreamArgs()", sshDownstreamArgs(session), wantPTY)

	session.Connection = connectionSpec{Profile: "myserver"}
	wantProfile := []string{
		"-S", "/tmp/control socket",
		"-o", "ControlMaster=no",
		"-o", "BatchMode=yes",
		"-o", "ProxyCommand=false",
		"-tt", "--", "myserver",
	}
	assertArgs("sshDownstreamArgs()", sshDownstreamArgs(session), wantProfile)

	remoteCommand := `printf '%s\n' "hello world"`
	command := sshCommandContext(context.Background(), append(sshDownstreamArgs(session), remoteCommand)...)
	if got := command.Args[len(command.Args)-1]; got != remoteCommand {
		t.Fatalf("remote command argument = %q, want %q", got, remoteCommand)
	}
}

func TestSSHCommandContextBoundsInheritedOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bin := t.TempDir()
	script := "#!/bin/sh\ntrap '' HUP\nsleep 2 &\nprintf x >&3\nwait\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = readyRead.Close()
	}()

	cmd := sshCommandContext(ctx)
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
