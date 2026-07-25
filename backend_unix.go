//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var errInvalidSSHCommand = errors.New("ssh command must start with 'ssh ' and include a destination")

const childIOWaitDelay = 100 * time.Millisecond

func platformOpenHelp() string {
	return `
当前平台使用系统 OpenSSH，并保留连接命令中的参数和配置。
`
}

func injectSSH(command string, options ...string) (string, error) {
	if len(command) < 4 || !strings.HasPrefix(command, "ssh") {
		return "", errInvalidSSHCommand
	}
	separator := command[3]
	if (separator != ' ' && separator != '\t') || strings.TrimSpace(command[3:]) == "" {
		return "", errInvalidSSHCommand
	}

	quoted := make([]string, len(options))
	for index, option := range options {
		quoted[index] = shellQuote(option)
	}
	return "ssh " + strings.Join(quoted, " ") + command[3:], nil
}

func validateOpenCommand(command string) error {
	_, err := injectSSH(command)
	return err
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func shellCommandContext(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exec "+command)
	cmd.WaitDelay = childIOWaitDelay
	return cmd
}

func masterCommand(session *session) (string, error) {
	return injectSSH(session.Command, "-M", "-S", session.Platform.ControlPath, "-tt")
}

func downstreamCommand(session *session) (string, error) {
	options := []string{
		"-S", session.Platform.ControlPath,
		"-o", "ControlMaster=no",
		"-o", "BatchMode=yes",
		"-o", "ProxyCommand=false",
	}
	if session.Mode == modeShellPTY {
		options = append(options, "-tt")
	}
	return injectSSH(session.Command, options...)
}

func runControlCommand(session *session, operation string) ([]byte, error) {
	command, err := injectSSH(session.Command,
		"-S", session.Platform.ControlPath,
		"-O", operation,
		"-o", "BatchMode=yes",
	)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return shellCommandContext(ctx, command).CombinedOutput()
}

func executeSession(
	session *session,
	remoteCommand string,
	timeout time.Duration,
	emit outputSink,
) (runStatus, error) {
	if strings.TrimSpace(remoteCommand) == "" {
		return runStatus{}, errors.New("command must not be empty")
	}
	if strings.ContainsRune(remoteCommand, 0) {
		return runStatus{}, errors.New("command must not contain NUL")
	}
	if err := checkSession(session); err != nil {
		return runStatus{}, err
	}

	command, err := downstreamCommand(session)
	if err != nil {
		return runStatus{}, err
	}
	status := runStatus{Session: session.ID, Mode: session.Mode}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if session.Mode == modeExec {
		command += " " + shellQuote(remoteCommand)
	}
	cmd := shellCommandContext(ctx, command)
	streams := newSerializedOutput(emit)
	if session.Mode == modeShellPTY {
		cmd.Stdin = strings.NewReader(strings.TrimRight(remoteCommand, "\n") + "\nexit\n")
		writer := streams.writer(streamOutput)
		cmd.Stdout, cmd.Stderr = writer, writer
	} else {
		cmd.Stdout = streams.writer(streamStdout)
		cmd.Stderr = streams.writer(streamStderr)
	}
	// Run waits for its output copies, so no emit call outlives it.
	err = cmd.Run()

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		status.TimedOut = true
		return status, nil
	}
	if outputErr := streams.failure(); outputErr != nil {
		return runStatus{}, outputErr
	}
	if err == nil {
		exitCode := 0
		status.ExitCode = &exitCode
		return status, nil
	}
	if exitError, ok := errors.AsType[*exec.ExitError](err); ok {
		exitCode := exitError.ExitCode()
		status.ExitCode = &exitCode
		return status, nil
	}
	return runStatus{}, err
}

func checkSession(session *session) error {
	output, err := runControlCommand(session, "check")
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	return &sessionUnavailableError{message: fmt.Sprintf("session %s is unavailable: %s", session.ID, message)}
}

func closeSession(session *session) error {
	_, _ = runControlCommand(session, "exit")

	if processAlive(session.PID) {
		if err := unix.Kill(session.PID, unix.SIGTERM); err != nil && !errors.Is(err, unix.ESRCH) {
			return err
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for processAlive(session.PID) {
		if time.Now().After(deadline) {
			return fmt.Errorf("session %s did not close", session.ID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

func terminateProcess(process *os.Process) {
	_ = process.Signal(unix.SIGTERM)
}
