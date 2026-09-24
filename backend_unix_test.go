//go:build linux || darwin

package main

import (
	"cmp"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSSHDownstreamOverridesLoginConfig(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH client is not installed")
	}
	for _, test := range []struct {
		mode       executionMode
		configured string
		want       string
	}{
		{modeExec, "force", "false"},
		{modeShellPTY, "no", "force"},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			config := filepath.Join(t.TempDir(), "ssh_config")
			remoteCommand := "cd /workspace && exec sh -l"
			configuration := "Host fixture\n  RequestTTY " + test.configured + "\n  RemoteCommand " + remoteCommand + "\n"
			if err := os.WriteFile(config, []byte(configuration), 0o600); err != nil {
				t.Fatal(err)
			}
			session := &session{Connection: connectionSpec{Profile: "fixture"}, Mode: test.mode}
			args := append([]string{"-G", "-F", config}, sshDownstreamArgs(session)...)
			if test.mode == modeExec {
				args = append(args, "printf test")
			}
			output, err := exec.Command(ssh, args...).CombinedOutput()
			if err != nil {
				t.Fatalf("ssh -G: %v\n%s", err, output)
			}
			if !strings.Contains(string(output), "\nrequesttty "+test.want+"\n") {
				t.Fatalf("ssh -G did not select RequestTTY %s:\n%s", test.want, output)
			}
			if strings.Contains(string(output), "\nremotecommand ") {
				t.Fatalf("ssh -G retained the login RemoteCommand:\n%s", output)
			}
			masterArgs := append([]string{"-G", "-F", config}, sshMasterArgs(session)...)
			masterOutput, err := exec.Command(ssh, masterArgs...).CombinedOutput()
			if err != nil {
				t.Fatalf("master ssh -G: %v\n%s", err, masterOutput)
			}
			if !strings.Contains(string(masterOutput), "\nremotecommand "+remoteCommand+"\n") {
				t.Fatalf("master ssh -G lost the login RemoteCommand:\n%s", masterOutput)
			}
		})
	}
}

func TestSSHSessionCommandsStayForegroundInteractive(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH client is not installed")
	}
	config := filepath.Join(t.TempDir(), "ssh_config")
	configuration := "Host fixture\n  ControlPersist yes\n  ForkAfterAuthentication yes\n  StdinNull yes\n"
	if err := os.WriteFile(config, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &session{Connection: connectionSpec{Profile: "fixture"}, Mode: modeExec}
	for _, test := range []struct {
		name string
		args []string
	}{
		{"master", sshMasterArgs(session)},
		{"downstream", append(sshDownstreamArgs(session), "printf test")},
		{"control", sshControlArgs(session, "check")},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"-G", "-F", config}, test.args...)
			output, err := exec.Command(ssh, args...).CombinedOutput()
			if err != nil {
				t.Fatalf("ssh -G: %v\n%s", err, output)
			}
			for _, option := range []string{"controlpersist", "forkafterauthentication", "stdinnull"} {
				if !strings.Contains(string(output), "\n"+option+" no\n") {
					t.Fatalf("ssh -G did not disable %s:\n%s", option, output)
				}
			}
		})
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
		emitErr     error
		wantOutput  map[outputStream]string
		wantExit    int
		wantTimeout bool
		wantWarning string
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
			wantOutput: map[outputStream]string{streamOutput: "startup failed\n"},
			wantExit:   255,
		},
		{
			name:        "timeout",
			mode:        modeExec,
			command:     "timeout",
			timeout:     20 * time.Millisecond,
			wantExit:    -1,
			wantTimeout: true,
			wantWarning: timeoutWarning,
		},
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
			status, err := executeSession(session, test.command, test.timeout, func(stream outputStream, data []byte) error {
				if test.emitErr != nil {
					return test.emitErr
				}
				output[stream] += string(data)
				return nil
			})
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
				exitCode != test.wantExit || status.TimedOut != test.wantTimeout ||
				status.Warning != test.wantWarning {
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
*" -O exit "*" close-failure "*) printf 'control exit failed\n' >&2; exit 255 ;;
*" -O exit "*) exit 0 ;;
*" exec-result "*) printf 'server\n'; printf 'warning\n' >&2; exit 7 ;;
*" startup-failure "*) printf 'startup failed\n' >&2; exit 255 ;;
*" timeout "*) sleep 5 ;;
*" output-timeout "*) printf x; sleep 5 ;;
*"ssh-handoff-copy-test"*) for command do :; done; exec sh -c "$command" ;;
*"ssh-handoff-copy-failure"*) printf partial; printf 'copy failed\n' >&2; exit 9 ;;
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
