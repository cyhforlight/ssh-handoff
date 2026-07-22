//go:build linux || darwin

package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const (
	keepaliveInterval = 30 * time.Minute
	handoffByte       = byte(0x1d) // Ctrl-]
	noopCommand       = ":\n"
)

type handoffController struct {
	remote  io.Writer
	managed bool
}

func (controller *handoffController) handleInput(input []byte) (bool, error) {
	changed := false
	segmentStart := 0
	write := func(segment []byte) error {
		if len(segment) == 0 {
			return nil
		}
		_, err := controller.remote.Write(segment)
		return err
	}

	for index, character := range input {
		if character != handoffByte {
			continue
		}
		if !controller.managed {
			if err := write(input[segmentStart:index]); err != nil {
				return changed, err
			}
		}
		if _, err := io.WriteString(controller.remote, noopCommand); err != nil {
			return changed, err
		}
		controller.managed = !controller.managed
		changed = true
		segmentStart = index + 1
	}
	if controller.managed {
		return changed, nil
	}
	return changed, write(input[segmentStart:])
}

func (controller *handoffController) keepalive() error {
	if !controller.managed {
		return nil
	}
	_, err := io.WriteString(controller.remote, noopCommand)
	return err
}

func serveOpenSession(registry *sessionRegistry, session *session, stdin *os.File, stdout, stderr io.Writer) int {
	if !term.IsTerminal(int(stdin.Fd())) {
		writeText(stderr, "ssh-handoff open: stdin must be a terminal\n")
		return 2
	}
	command, err := masterCommand(session)
	if err != nil {
		writeTextf(stderr, "ssh-handoff open: %v\n", err)
		return 2
	}
	_ = os.Remove(session.Platform.ControlPath)
	cmd := exec.Command("/bin/sh", "-c", "exec "+command)
	terminal, err := pty.Start(cmd)
	if err != nil {
		writeTextf(stderr, "ssh-handoff open: %v\n", err)
		return 2
	}
	defer func() { _ = terminal.Close() }()
	session.State = stateInteractive
	if err := registry.update(session); err != nil {
		terminateProcess(cmd.Process)
		writeTextf(stderr, "ssh-handoff open: %v\n", err)
		return 2
	}

	previousState, err := term.MakeRaw(int(stdin.Fd()))
	if err != nil {
		terminateProcess(cmd.Process)
		writeTextf(stderr, "ssh-handoff open: %v\n", err)
		return 2
	}
	defer term.Restore(int(stdin.Fd()), previousState) //nolint:errcheck // best effort during terminal teardown
	_ = pty.InheritSize(stdin, terminal)

	input := make(chan []byte, 1)
	inputErrors := make(chan error, 1)
	go readTerminalInput(stdin, input, inputErrors)

	outputDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(stdout, terminal)
		close(outputDone)
	}()

	processDone := make(chan error, 1)
	go func() {
		waitErr := cmd.Wait()
		<-outputDone
		processDone <- waitErr
	}()

	resize := make(chan os.Signal, 1)
	signal.Notify(resize, unix.SIGWINCH)
	defer signal.Stop(resize)

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, unix.SIGTERM, unix.SIGHUP)
	defer signal.Stop(shutdown)

	controller := handoffController{remote: terminal}
	var keepalive *time.Ticker
	var keepaliveTick <-chan time.Time
	defer func() {
		if keepalive != nil {
			keepalive.Stop()
		}
	}()

	for {
		select {
		case data := <-input:
			changed, inputErr := controller.handleInput(data)
			if inputErr != nil {
				terminateProcess(cmd.Process)
				writeTextf(stderr, "\r\nssh-handoff: write session: %v\r\n", inputErr)
				return 2
			}
			if !changed {
				continue
			}
			if keepalive != nil {
				keepalive.Stop()
			}
			if controller.managed {
				session.State = stateManaged
				keepalive = time.NewTicker(keepaliveInterval)
				keepaliveTick = keepalive.C
				writeText(stderr, "\r\n[ssh-handoff] 已托管；按 Ctrl-] 恢复交互。\r\n")
			} else {
				session.State = stateInteractive
				keepalive = nil
				keepaliveTick = nil
				writeText(stderr, "\r\n[ssh-handoff] 已恢复交互；按 Ctrl-] 再次托管。\r\n")
			}
			if err := registry.update(session); err != nil {
				terminateProcess(cmd.Process)
				writeTextf(stderr, "\r\nssh-handoff: %v\r\n", err)
				return 2
			}
		case <-keepaliveTick:
			if err := controller.keepalive(); err != nil {
				terminateProcess(cmd.Process)
				writeTextf(stderr, "\r\nssh-handoff: keepalive: %v\r\n", err)
				return 2
			}
		case <-resize:
			_ = pty.InheritSize(stdin, terminal)
		case <-shutdown:
			terminateProcess(cmd.Process)
		case inputErr := <-inputErrors:
			if inputErr != nil && !errors.Is(inputErr, io.EOF) {
				writeTextf(stderr, "\r\nssh-handoff: %v\r\n", inputErr)
			}
			terminateProcess(cmd.Process)
		case waitErr := <-processDone:
			if waitErr == nil {
				return 0
			}
			if exitError, ok := errors.AsType[*exec.ExitError](waitErr); ok {
				return exitError.ExitCode()
			}
			writeTextf(stderr, "\r\nssh-handoff: %v\r\n", waitErr)
			return 2
		}
	}
}

func readTerminalInput(input *os.File, output chan<- []byte, failures chan<- error) {
	buffer := make([]byte, 4*1024)
	for {
		count, err := input.Read(buffer)
		if count > 0 {
			data := append([]byte(nil), buffer[:count]...)
			output <- data
		}
		if err != nil {
			failures <- err
			return
		}
	}
}
