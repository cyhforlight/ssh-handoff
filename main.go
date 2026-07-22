package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

const defaultTimeout = time.Minute

type sessionUnavailableError struct {
	message string
}

func (err *sessionUnavailableError) Error() string {
	return err.message
}

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func runCLI(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeText(stderr, "usage: ssh-handoff <open|run|list|close> ...\n")
		return 2
	}

	registry, err := openRegistry()
	if err != nil {
		return writeCommandError(args[0], stdout, stderr, "local_error", err)
	}

	switch args[0] {
	case "open":
		return openCommand(registry, args[1:], stdin, stdout, stderr)
	case "run":
		return runCommand(registry, args[1:], stdout)
	case "list":
		return listCommand(registry, args[1:], stdout)
	case "close":
		return closeCommand(registry, args[1:], stdout)
	default:
		writeTextf(stderr, "ssh-handoff: unknown command %q\n", args[0])
		return 2
	}
}

func openCommand(registry *sessionRegistry, args []string, stdin *os.File, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("open", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("name", "", "session name")
	modeText := flags.String("mode", string(modeExec), "exec or shell-pty")
	if err := flags.Parse(args); err != nil {
		writeTextf(stderr, "ssh-handoff open: %v\n", err)
		return 2
	}
	if flags.NArg() != 1 {
		writeText(stderr, "usage: ssh-handoff open [--name NAME] [--mode exec|shell-pty] 'ssh ...'\n")
		return 2
	}

	mode, err := parseMode(*modeText)
	if err != nil {
		writeTextf(stderr, "ssh-handoff open: %v\n", err)
		return 2
	}
	command := flags.Arg(0)
	if err := validateOpenCommand(command); err != nil {
		writeTextf(stderr, "ssh-handoff open: %v\n", err)
		return 2
	}

	session, err := registry.create(*name, mode, command)
	if err != nil {
		writeTextf(stderr, "ssh-handoff open: %v\n", err)
		return 2
	}
	defer registry.remove(session.ID)

	writeTextf(stderr, "[ssh-handoff] session %s", session.ID)
	if session.Name != "" {
		writeTextf(stderr, " (%s)", session.Name)
	}
	writeText(stderr, "\n[ssh-handoff] 登录并进入空闲 Shell 后，按 Ctrl-] 切换托管。\n")

	return serveOpenSession(registry, session, stdin, stdout, stderr)
}

func runCommand(registry *sessionRegistry, args []string, stdout io.Writer) int {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	timeout := flags.Duration("timeout", defaultTimeout, "command timeout")
	if err := flags.Parse(args); err != nil {
		return writeJSONError(stdout, "invalid_arguments", err)
	}
	if flags.NArg() != 2 {
		return writeJSONError(stdout, "invalid_arguments", errors.New("usage: ssh-handoff run [--timeout DURATION] <session> '<command>'"))
	}
	if *timeout <= 0 {
		return writeJSONError(stdout, "invalid_arguments", errors.New("timeout must be greater than zero"))
	}

	session, err := registry.resolve(flags.Arg(0))
	if err != nil {
		return writeSessionError(stdout, err)
	}

	var result runResult
	err = registry.withSessionLock(session.ID, func() error {
		var runErr error
		result, runErr = executeSession(session, flags.Arg(1), *timeout)
		return runErr
	})
	if err != nil {
		return writeCommandFailure(stdout, err)
	}
	writeJSON(stdout, result)
	if result.TimedOut {
		return 124
	}
	if result.ExitCode != nil && *result.ExitCode != 0 {
		if *result.ExitCode > 0 && *result.ExitCode <= 255 {
			return *result.ExitCode
		}
		return 1
	}
	return 0
}

func listCommand(registry *sessionRegistry, args []string, stdout io.Writer) int {
	if len(args) != 0 {
		return writeJSONError(stdout, "invalid_arguments", errors.New("usage: ssh-handoff list"))
	}
	sessions, err := registry.list()
	if err != nil {
		return writeJSONError(stdout, "local_error", err)
	}
	summaries := make([]sessionSummary, len(sessions))
	for index, session := range sessions {
		summaries[index] = session.summary()
	}
	writeJSON(stdout, struct {
		Sessions []sessionSummary `json:"sessions"`
	}{Sessions: summaries})
	return 0
}

func closeCommand(registry *sessionRegistry, args []string, stdout io.Writer) int {
	if len(args) != 1 {
		return writeJSONError(stdout, "invalid_arguments", errors.New("usage: ssh-handoff close <session>"))
	}
	session, err := registry.resolve(args[0])
	if err != nil {
		return writeSessionError(stdout, err)
	}
	err = registry.withSessionLock(session.ID, func() error {
		return closeSession(session)
	})
	if err != nil {
		return writeCommandFailure(stdout, err)
	}
	writeJSON(stdout, struct {
		Session string `json:"session"`
		Closed  bool   `json:"closed"`
	}{Session: session.ID, Closed: true})
	return 0
}

func writeCommandError(command string, stdout, stderr io.Writer, code string, err error) int {
	if command == "run" || command == "list" || command == "close" {
		return writeJSONError(stdout, code, err)
	}
	writeTextf(stderr, "ssh-handoff: %v\n", err)
	return 2
}

func writeSessionError(stdout io.Writer, err error) int {
	if errors.Is(err, errSessionNotFound) {
		return writeJSONError(stdout, "session_not_found", err)
	}
	return writeJSONError(stdout, "local_error", err)
}

func writeCommandFailure(stdout io.Writer, err error) int {
	if _, ok := errors.AsType[*sessionUnavailableError](err); ok {
		return writeJSONError(stdout, "session_unavailable", err)
	}
	return writeJSONError(stdout, "execution_error", err)
}

func writeJSONError(stdout io.Writer, code string, err error) int {
	writeJSON(stdout, struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: err.Error()}})
	return 2
}

func writeJSON(output io.Writer, value any) {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func writeText(output io.Writer, value string) {
	_, _ = io.WriteString(output, value)
}

func writeTextf(output io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(output, format, args...)
}
