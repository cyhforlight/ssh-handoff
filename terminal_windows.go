//go:build windows

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

type windowsProcessResult struct {
	exitCode int
	err      error
}

func serveOpenSession(
	registry *sessionRegistry,
	session *session,
	stdin *os.File,
	stdout,
	stderr io.Writer,
) int {
	if !term.IsTerminal(int(stdin.Fd())) {
		writeText(stderr, "ssh-handoff open: stdin must be a terminal\n")
		return 2
	}
	path, err := resolvePlinkPath()
	if err != nil {
		writeTextf(stderr, "ssh-handoff open: %v\n", err)
		return 2
	}
	session.Platform.PlinkPath = path
	if err := createPlinkProfiles(session.ID); err != nil {
		writeTextf(stderr, "ssh-handoff open: %v\n", err)
		return 2
	}

	checkContext, cancelCheck := context.WithTimeout(context.Background(), 5*time.Second)
	exists, _, err := plinkShareExists(checkContext, session)
	cancelCheck()
	if err != nil {
		writeTextf(stderr, "ssh-handoff open: check existing Plink connection: %v\n", err)
		return 2
	}
	if exists {
		writeTextf(
			stderr,
			"ssh-handoff open: a shared Plink connection already exists for %s\n",
			session.Connection.targetLabel(),
		)
		return 2
	}

	consoleOutput, consoleOutputErr := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if consoleOutputErr == nil {
		defer func() { _ = consoleOutput.Close() }()
	}
	width, height := 80, 24
	if consoleOutputErr == nil {
		if currentWidth, currentHeight, sizeErr := term.GetSize(int(consoleOutput.Fd())); sizeErr == nil {
			width, height = currentWidth, currentHeight
		}
	}
	restoreCodePages, err := useUTF8Console()
	if err != nil {
		writeTextf(stderr, "ssh-handoff open: configure UTF-8 console: %v\n", err)
		return 2
	}
	defer restoreCodePages()

	terminal, err := startWindowsPTY(
		session.Platform.PlinkPath,
		plinkMasterArgs(session),
		width,
		height,
	)
	if err != nil {
		writeTextf(stderr, "ssh-handoff open: %v\n", err)
		return 2
	}
	defer terminal.Close()
	session.Platform.PlinkPID = terminal.PID()

	outputDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(stdout, terminal.Output)
		close(outputDone)
	}()
	processDone := make(chan windowsProcessResult, 1)
	go func() {
		exitCode, waitErr := terminal.Wait()
		<-outputDone
		processDone <- windowsProcessResult{exitCode: exitCode, err: waitErr}
	}()

	if err := waitForPlinkShare(session, 2*time.Second); err != nil {
		terminal.Terminate()
		<-processDone
		writeTextf(stderr, "ssh-handoff open: Plink connection sharing is unavailable: %v\n", err)
		return 2
	}

	if err := registry.update(session); err != nil {
		terminal.Terminate()
		<-processDone
		writeTextf(stderr, "ssh-handoff open: %v\n", err)
		return 2
	}

	previousState, err := term.MakeRaw(int(stdin.Fd()))
	if err != nil {
		terminal.Terminate()
		<-processDone
		writeTextf(stderr, "ssh-handoff open: %v\n", err)
		return 2
	}
	defer term.Restore(int(stdin.Fd()), previousState) //nolint:errcheck // best effort during terminal teardown

	input := make(chan []byte, 1)
	inputErrors := make(chan error, 1)
	go readTerminalInput(stdin, input, inputErrors)

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt)
	defer signal.Stop(shutdown)

	resize := time.NewTicker(250 * time.Millisecond)
	defer resize.Stop()
	lastWidth, lastHeight := width, height

	controller := handoffController{remote: terminal.Input}
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
				terminal.Terminate()
				<-processDone
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
				keepalive = time.NewTicker(keepaliveInterval)
				keepaliveTick = keepalive.C
				writeTextf(stderr, "\r\n[ssh-handoff] %s 已托管；按 Ctrl-] 恢复交互。\r\n", session.ID)
			} else {
				keepalive = nil
				keepaliveTick = nil
				writeTextf(stderr, "\r\n[ssh-handoff] %s 已恢复交互；按 Ctrl-] 再次托管。\r\n", session.ID)
			}
		case <-keepaliveTick:
			if err := controller.keepalive(); err != nil {
				terminal.Terminate()
				<-processDone
				writeTextf(stderr, "\r\nssh-handoff: keepalive: %v\r\n", err)
				return 2
			}
		case <-resize.C:
			if consoleOutput != nil {
				width, height, sizeErr := term.GetSize(int(consoleOutput.Fd()))
				if sizeErr == nil && (width != lastWidth || height != lastHeight) {
					if err := terminal.Resize(width, height); err == nil {
						lastWidth, lastHeight = width, height
					}
				}
			}
		case <-shutdown:
			terminal.Terminate()
		case inputErr := <-inputErrors:
			if inputErr != nil && !errors.Is(inputErr, io.EOF) {
				writeTextf(stderr, "\r\nssh-handoff: %v\r\n", inputErr)
			}
			terminal.Terminate()
		case result := <-processDone:
			if result.err != nil {
				writeTextf(stderr, "\r\nssh-handoff: %v\r\n", result.err)
				return 2
			}
			if result.exitCode >= 0 && result.exitCode <= 255 {
				return result.exitCode
			}
			return 1
		}
	}
}

func useUTF8Console() (func(), error) {
	inputCodePage, err := windows.GetConsoleCP()
	if err != nil {
		return nil, err
	}
	outputCodePage, err := windows.GetConsoleOutputCP()
	if err != nil {
		return nil, err
	}
	if err := windows.SetConsoleCP(65001); err != nil {
		return nil, err
	}
	if err := windows.SetConsoleOutputCP(65001); err != nil {
		_ = windows.SetConsoleCP(inputCodePage)
		return nil, err
	}
	return func() {
		_ = windows.SetConsoleCP(inputCodePage)
		_ = windows.SetConsoleOutputCP(outputCodePage)
	}, nil
}
