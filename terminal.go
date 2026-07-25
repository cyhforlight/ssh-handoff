package main

import (
	"io"
	"os"
	"time"
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
