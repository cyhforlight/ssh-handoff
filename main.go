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

const (
	openUsage  = "ssh-handoff open [--note NOTE] [--mode exec|shell-pty] 'ssh ...'"
	runUsage   = "ssh-handoff run [--timeout DURATION] <session-id> '<command>'"
	listUsage  = "ssh-handoff list"
	closeUsage = "ssh-handoff close <session-id>"
)

type sessionUnavailableError struct {
	message string
}

func (err *sessionUnavailableError) Error() string {
	return err.message
}

type commandError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	Error commandError `json:"error"`
}

type runResult struct {
	Session  string        `json:"session"`
	Mode     executionMode `json:"mode"`
	Stdout   *string       `json:"stdout,omitempty"`
	Stderr   *string       `json:"stderr,omitempty"`
	Output   *string       `json:"output,omitempty"`
	ExitCode *int          `json:"exit_code"`
	TimedOut bool          `json:"timed_out"`
}

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func runCLI(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeText(stderr, "usage: ssh-handoff <open|run|list|close> ...\n")
		return 2
	}
	if len(args) == 1 && isHelpFlag(args[0]) {
		return writeHelp("", stdout, stderr)
	}
	if len(args) == 2 && isHelpFlag(args[1]) {
		return writeHelp(args[0], stdout, stderr)
	}

	registry, err := openRegistry()
	if err != nil {
		return writeCommandError(args[0], stdout, stderr, err)
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
	note := flags.String("note", "", "session note")
	modeText := flags.String("mode", string(modeExec), "exec or shell-pty")
	if err := flags.Parse(args); err != nil {
		writeTextf(stderr, "ssh-handoff open: %v\n", err)
		return 2
	}
	if flags.NArg() != 1 {
		writeTextf(stderr, "usage: %s\n", openUsage)
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

	session, err := registry.create(*note, mode, command)
	if err != nil {
		writeTextf(stderr, "ssh-handoff open: %v\n", err)
		return 2
	}
	defer registry.remove(session.ID)

	writeTextf(stderr, "[ssh-handoff] session %s\n", session.ID)
	if session.Note != "" {
		writeTextf(stderr, "[ssh-handoff] note: %s\n", session.Note)
	}
	writeText(stderr, "[ssh-handoff] 登录并进入空闲 Shell 后，按 Ctrl-] 切换托管。\n")

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
		return writeJSONError(stdout, "invalid_arguments", errors.New("usage: "+runUsage))
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
		return writeJSONError(stdout, "invalid_arguments", errors.New("usage: "+listUsage))
	}
	sessions, err := registry.list()
	if err != nil {
		return writeJSONError(stdout, "local_error", err)
	}
	summaries := make([]sessionInfo, len(sessions))
	for index, session := range sessions {
		summaries[index] = session.sessionInfo
	}
	writeJSON(stdout, struct {
		Sessions []sessionInfo `json:"sessions"`
	}{Sessions: summaries})
	return 0
}

func closeCommand(registry *sessionRegistry, args []string, stdout io.Writer) int {
	if len(args) != 1 {
		return writeJSONError(stdout, "invalid_arguments", errors.New("usage: "+closeUsage))
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

func isHelpFlag(argument string) bool {
	return argument == "-h" || argument == "--help"
}

func writeHelp(command string, stdout, stderr io.Writer) int {
	text, ok := helpText(command)
	if !ok {
		writeTextf(stderr, "ssh-handoff: unknown command %q\n", command)
		return 2
	}
	writeText(stdout, text)
	return 0
}

func helpText(command string) (string, bool) {
	switch command {
	case "":
		return `ssh-handoff 复用由用户完成认证的 SSH 连接，供 Agent 执行命令。

用法:
  ` + openUsage + `
  ` + runUsage + `
  ` + listUsage + `
  ` + closeUsage + `

命令:
  open   建立连接并保持原始 Shell
  run    通过指定 session 执行一条命令
  list   列出仍然存活的 session
  close  关闭指定 session

使用 "ssh-handoff <command> --help" 查看子命令帮助。
`, true
	case "open":
		return `建立 SSH 连接，由用户完成认证并保持原始 Shell。

用法:
  ` + openUsage + `

选项:
  --note NOTE     添加用于辨认 session 的备注
  --mode MODE     执行模式：exec（默认）或 shell-pty

进入空闲 Shell 后按 Ctrl-] 切换托管；再次按下恢复交互。
`, true
	case "run":
		return `通过已有 session 的新 channel 同步执行一条命令。

用法:
  ` + runUsage + `

选项:
  --timeout DURATION   超时时间（默认 1m）

session ID 输入不区分大小写。执行模式由 open 时的 --mode 决定。
`, true
	case "list":
		return `列出仍然存活的 session。

用法:
  ` + listUsage + `

结果以 JSON 输出。
`, true
	case "close":
		return `关闭指定 session。

用法:
  ` + closeUsage + `

session ID 输入不区分大小写，结果以 JSON 输出。
`, true
	default:
		return "", false
	}
}

func writeCommandError(command string, stdout, stderr io.Writer, err error) int {
	if command == "run" || command == "list" || command == "close" {
		return writeJSONError(stdout, "local_error", err)
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
	writeJSON(stdout, errorResponse{Error: commandError{Code: code, Message: err.Error()}})
	return 2
}

func writeJSON(output io.Writer, value any) {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}

func writeText(output io.Writer, value string) {
	_, _ = io.WriteString(output, value)
}

func writeTextf(output io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(output, format, args...)
}
