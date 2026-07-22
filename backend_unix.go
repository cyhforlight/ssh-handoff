//go:build linux || darwin

package main

import (
	"bytes"
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
	return exec.CommandContext(ctx, "/bin/sh", "-c", "exec "+command).CombinedOutput()
}

func executeSession(session *session, remoteCommand string, timeout time.Duration) (runResult, error) {
	if strings.TrimSpace(remoteCommand) == "" {
		return runResult{}, errors.New("command must not be empty")
	}
	if strings.ContainsRune(remoteCommand, 0) {
		return runResult{}, errors.New("command must not contain NUL")
	}
	if err := checkSession(session); err != nil {
		return runResult{}, err
	}

	command, err := downstreamCommand(session)
	if err != nil {
		return runResult{}, err
	}
	result := runResult{Session: session.ID, Mode: session.Mode}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if session.Mode == modeExec {
		command += " " + shellQuote(remoteCommand)
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exec "+command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	if session.Mode == modeShellPTY {
		cmd.Stdin = strings.NewReader(strings.TrimRight(remoteCommand, "\n") + "\nexit\n")
		cmd.Stderr = &stdout
	} else {
		cmd.Stderr = &stderr
	}
	err = cmd.Run()
	if session.Mode == modeExec {
		stdoutText, stderrText := stdout.String(), stderr.String()
		result.Stdout, result.Stderr = &stdoutText, &stderrText
	} else {
		output := stdout.String()
		result.Output = &output
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
		return result, nil
	}
	if err == nil {
		exitCode := 0
		result.ExitCode = &exitCode
		return result, nil
	}
	if exitError, ok := errors.AsType[*exec.ExitError](err); ok {
		exitCode := exitError.ExitCode()
		result.ExitCode = &exitCode
		return result, nil
	}
	return runResult{}, err
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
