package main

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"time"
)

const childIOWaitDelay = 100 * time.Millisecond

func executeSession(
	session *session,
	remoteCommand string,
	timeout time.Duration,
	shellReadyDelay time.Duration,
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

	status := runStatus{Session: session.ID, Mode: session.Mode}
	if session.Mode == modeShellPTY {
		timeout += shellReadyDelay
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd, cleanup, err := newSessionCommand(ctx, session, remoteCommand)
	if err != nil {
		return runStatus{}, err
	}
	if cleanup != nil {
		defer cleanup()
	}

	streams := newSerializedOutput(emit)
	var inputErr error
	if session.Mode == modeShellPTY {
		writer := streams.writer(streamOutput)
		cmd.Stdout, cmd.Stderr = writer, writer
		input, pipeErr := cmd.StdinPipe()
		if pipeErr != nil {
			return runStatus{}, pipeErr
		}
		err = cmd.Start()
		if err == nil {
			processDone := make(chan error, 1)
			go func() { processDone <- cmd.Wait() }()
			if _, inputErr = io.WriteString(input, "\n"); inputErr == nil {
				timer := time.NewTimer(shellReadyDelay)
				select {
				case err = <-processDone:
					timer.Stop()
				case <-timer.C:
					_, inputErr = io.WriteString(
						input,
						strings.TrimRight(remoteCommand, "\n")+"\nexit\n",
					)
					_ = input.Close()
					err = <-processDone
				}
			} else {
				_ = input.Close()
				err = <-processDone
			}
		}
		_ = input.Close()
	} else {
		cmd.Stdout = streams.writer(streamStdout)
		cmd.Stderr = streams.writer(streamStderr)
		// Run waits for its output copies, so no emit call outlives it.
		err = cmd.Run()
	}

	if outputErr := streams.failure(); outputErr != nil {
		return runStatus{}, outputErr
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		status.TimedOut = true
		return status, nil
	}
	exitCode := 0
	if err != nil {
		exitError, ok := errors.AsType[*exec.ExitError](err)
		if !ok {
			return runStatus{}, err
		}
		exitCode = exitError.ExitCode()
	} else if inputErr != nil {
		return runStatus{}, inputErr
	}
	status.ExitCode = &exitCode
	return status, nil
}
