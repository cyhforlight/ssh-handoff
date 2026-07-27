//go:build linux || darwin

package main

import (
	"cmp"
	"context"
	"errors"
	"io"
	"maps"
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
		Connection: connectionSpec{
			Host:     "2001:db8::10",
			User:     "operator",
			Port:     2222,
			Identity: "/keys/work key",
		},
		Mode:     modeExec,
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

func TestExecuteSession(t *testing.T) {
	installFakeSSH(t)
	outputErr := errors.New("output failed")
	tests := []struct {
		name        string
		mode        executionMode
		host        string
		command     string
		timeout     time.Duration
		readyDelay  time.Duration
		wantBefore  time.Duration
		emitErr     error
		wantOutput  map[outputStream]string
		wantExit    int
		wantTimeout bool
		wantErr     error
	}{
		{
			name:       "exec output and exit code",
			mode:       modeExec,
			command:    "exec-result",
			timeout:    time.Second,
			wantOutput: map[outputStream]string{streamStdout: "server\n", streamStderr: "warning\n"},
			wantExit:   7,
		},
		{
			name:       "shell PTY input and output",
			mode:       modeShellPTY,
			command:    "echo hello",
			timeout:    time.Second,
			wantOutput: map[outputStream]string{streamOutput: "hello\n"},
			wantExit:   0,
		},
		{
			name:       "shell PTY startup failure",
			mode:       modeShellPTY,
			host:       "startup-failure",
			command:    "ignored",
			timeout:    time.Second,
			readyDelay: 2 * time.Second,
			wantBefore: 2 * time.Second,
			wantOutput: map[outputStream]string{streamOutput: "startup failed\n"},
			wantExit:   255,
		},
		{name: "timeout", mode: modeExec, command: "timeout", timeout: 20 * time.Millisecond, wantExit: -1, wantTimeout: true},
		{name: "output error wins over timeout", mode: modeExec, command: "output-timeout", timeout: 20 * time.Millisecond, emitErr: outputErr, wantErr: outputErr},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := &session{
				ID:         "ABCD",
				Connection: connectionSpec{Host: cmp.Or(test.host, "example.com"), User: "operator", Port: 22},
				Mode:       test.mode,
				Platform:   platformSessionState{ControlPath: filepath.Join(t.TempDir(), "control")},
			}
			output := make(map[outputStream]string)
			started := time.Now()
			status, err := executeSession(session, test.command, test.timeout, test.readyDelay, func(stream outputStream, data []byte) error {
				if test.emitErr != nil {
					return test.emitErr
				}
				output[stream] += string(data)
				return nil
			})
			if test.wantBefore > 0 && time.Since(started) >= test.wantBefore {
				t.Fatalf("executeSession() did not stop the ready delay")
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("executeSession() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil {
				return
			}
			exitCode := -1
			if status.ExitCode != nil {
				exitCode = *status.ExitCode
			}
			if status.Session != session.ID || status.Mode != test.mode ||
				exitCode != test.wantExit || status.TimedOut != test.wantTimeout {
				t.Fatalf("executeSession() status = %#v", status)
			}
			if !maps.Equal(output, test.wantOutput) {
				t.Fatalf("executeSession() output = %#v, want %#v", output, test.wantOutput)
			}
		})
	}
}

func installFakeSSH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
case " $* " in
*" -O check "*) exit 0 ;;
*" exec-result "*) printf 'server\n'; printf 'warning\n' >&2; exit 7 ;;
*" startup-failure "*) printf 'startup failed\n' >&2; exit 255 ;;
*" timeout "*) sleep 5 ;;
*" output-timeout "*) printf x; sleep 5 ;;
*) while IFS= read -r line; do eval "$line"; done ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
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
