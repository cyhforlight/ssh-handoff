package main

import (
	"bytes"
	"io"
	"os"
	"time"
)

const (
	keepaliveInterval = 10 * time.Minute
	handoffByte       = byte(0x1d) // Ctrl-]
	noopCommand       = ":\n"
)

type handoffController struct {
	remote        io.Writer
	managed       bool
	keepaliveTick <-chan time.Time
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

	if !controller.managed {
		if err := write(input[segmentStart:]); err != nil {
			return changed, err
		}
	}
	if changed {
		controller.keepaliveTick = nil
		if controller.managed {
			controller.keepaliveTick = time.Tick(keepaliveInterval)
		}
	}
	return changed, nil
}

func (controller *handoffController) handoffMessage(sessionID string) string {
	if controller.managed {
		return "\r\n[ssh-handoff] " + sessionID + " 已托管；按 Ctrl-] 恢复交互。\r\n"
	}
	return "\r\n[ssh-handoff] " + sessionID + " 已恢复交互；按 Ctrl-] 再次托管。\r\n"
}

func (controller *handoffController) keepalive() error {
	if !controller.managed {
		return nil
	}
	_, err := io.WriteString(controller.remote, noopCommand)
	return err
}

func readTerminalInput(input *os.File, output chan<- []byte, failures chan<- error) {
	buffer := make([]byte, 4*1024)
	for {
		count, err := input.Read(buffer)
		if count > 0 {
			data := bytes.Clone(buffer[:count])
			output <- data
		}
		if err != nil {
			failures <- err
			return
		}
	}
}
