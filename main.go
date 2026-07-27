package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

const (
	defaultTimeout         = time.Minute
	defaultShellReadyDelay = time.Second
)

const (
	openUsage  = "ssh-handoff open --host HOST --user USER [--port PORT] [--identity FILE] [--mode exec|shell-pty] [--note NOTE]\n  ssh-handoff open --profile NAME [--mode exec|shell-pty] [--note NOTE]"
	runUsage   = "ssh-handoff run [--stream] [--timeout DURATION] [--shell-ready-delay DURATION] <session-id> <command|->"
	listUsage  = "ssh-handoff list"
	closeUsage = "ssh-handoff close <session-id>"
)

type openOptions struct {
	Connection connectionSpec
	Note       string
	Mode       executionMode
}

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
	if len(args) == 1 && isHelpFlag(args[0]) {
		return writeHelp("", stdout, stderr)
	}
	if len(args) == 2 && isHelpFlag(args[1]) {
		return writeHelp(args[0], stdout, stderr)
	}

	registry := newSessionRegistry()

	switch args[0] {
	case "open":
		return openCommand(registry, args[1:], stdin, stdout, stderr)
	case "run":
		return runCommand(registry, args[1:], stdin, stdout)
	case "list":
		return listCommand(registry, args[1:], stdout, stderr)
	case "close":
		return closeCommand(registry, args[1:], stdout, stderr)
	default:
		writeTextf(stderr, "ssh-handoff: unknown command %q\n", args[0])
		return 2
	}
}

func openCommand(registry *sessionRegistry, args []string, stdin *os.File, stdout, stderr io.Writer) int {
	options, err := parseOpenArguments(args)
	if err != nil {
		writeTextf(stderr, "ssh-handoff open: %v\n", err)
		return 2
	}

	session, err := registry.create(options.Note, options.Mode, options.Connection)
	if err != nil {
		writeTextf(stderr, "ssh-handoff open: %v\n", err)
		return 2
	}
	defer registry.removeFiles(session.ID)

	writeTextf(stderr, "[ssh-handoff] session %s\n", session.ID)
	if session.Note != "" {
		writeTextf(stderr, "[ssh-handoff] note: %s\n", session.Note)
	}
	writeText(stderr, "[ssh-handoff] 登录并进入空闲 Shell 后，按 Ctrl-] 切换托管。\n")

	return serveOpenSession(registry, session, stdin, stdout, stderr)
}

func parseOpenArguments(args []string) (openOptions, error) {
	flags := flag.NewFlagSet("open", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	host := flags.String("host", "", "SSH host")
	user := flags.String("user", "", "SSH user")
	port := flags.Int("port", 22, "SSH port")
	identity := flags.String("identity", "", "identity file")
	profile := flags.String("profile", "", "OpenSSH profile")
	note := flags.String("note", "", "session note")
	modeText := flags.String("mode", string(modeExec), "exec or shell-pty")
	if err := flags.Parse(args); err != nil {
		return openOptions{}, err
	}
	if flags.NArg() != 0 {
		return openOptions{}, errors.New("usage: " + openUsage)
	}

	mode, err := parseMode(*modeText)
	if err != nil {
		return openOptions{}, err
	}

	present := make(map[string]bool)
	flags.Visit(func(flag *flag.Flag) {
		present[flag.Name] = true
	})
	connection := connectionSpec{
		Host:     *host,
		User:     *user,
		Port:     *port,
		Identity: *identity,
	}
	if present["profile"] {
		if *profile == "" {
			return openOptions{}, errors.New("profile is required")
		}
		for _, name := range []string{"host", "user", "port", "identity"} {
			if present[name] {
				return openOptions{}, fmt.Errorf("--profile cannot be combined with --%s", name)
			}
		}
		connection = connectionSpec{Profile: *profile}
	}
	if err := connection.validate(); err != nil {
		return openOptions{}, err
	}
	return openOptions{Connection: connection, Note: *note, Mode: mode}, nil
}

func runCommand(registry *sessionRegistry, args []string, stdin io.Reader, stdout io.Writer) int {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	timeout := flags.Duration("timeout", defaultTimeout, "command timeout")
	shellReadyDelay := flags.Duration(
		"shell-ready-delay",
		defaultShellReadyDelay,
		"delay before sending a shell-pty command",
	)
	stream := flags.Bool("stream", false, "stream output as NDJSON")
	parseErr := flags.Parse(args)
	output := newRunOutput(stdout, *stream)
	if parseErr != nil {
		return writeRunError(output, "invalid_arguments", parseErr)
	}
	if flags.NArg() != 2 {
		return writeRunError(output, "invalid_arguments", errors.New("usage: "+runUsage))
	}
	if *timeout <= 0 {
		return writeRunError(output, "invalid_arguments", errors.New("timeout must be greater than zero"))
	}
	if *shellReadyDelay < 0 {
		return writeRunError(
			output,
			"invalid_arguments",
			errors.New("shell-ready-delay must not be negative"),
		)
	}

	session, err := registry.resolve(flags.Arg(0))
	if err != nil {
		return writeRunSessionError(output, err)
	}

	command := flags.Arg(1)
	if command == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return writeRunError(output, "local_error", fmt.Errorf("read command from stdin: %w", err))
		}
		command = normalizeStdinCommand(data)
	}

	status, err := executeSession(session, command, *timeout, *shellReadyDelay, output.emit)
	if err != nil {
		return writeRunExecutionError(output, err)
	}
	if err := output.writeResult(status); err != nil {
		return 2
	}
	if status.TimedOut {
		return 124
	}
	if status.ExitCode != nil && *status.ExitCode != 0 {
		if *status.ExitCode > 0 && *status.ExitCode <= 255 {
			return *status.ExitCode
		}
		return 1
	}
	return 0
}

func normalizeStdinCommand(data []byte) string {
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

func listCommand(registry *sessionRegistry, args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		writeTextf(stderr, "usage: %s\n", listUsage)
		return 2
	}
	sessions, err := registry.list()
	if err != nil {
		writeTextf(stderr, "ssh-handoff list: %v\n", err)
		return 2
	}

	writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	writeText(writer, "ID\tMODE\tCONNECTION\tNOTE\n")
	sanitize := strings.NewReplacer("\t", " ", "\r", " ", "\n", " ")
	for _, session := range sessions {
		writeTextf(writer, "%s\t%s\t%s\t%s\n",
			session.ID,
			session.Mode,
			sanitize.Replace(session.Connection.label()),
			sanitize.Replace(session.Note),
		)
	}
	_ = writer.Flush()
	return 0
}

func closeCommand(registry *sessionRegistry, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		writeTextf(stderr, "usage: %s\n", closeUsage)
		return 2
	}
	session, err := registry.resolve(args[0])
	if err != nil {
		writeTextf(stderr, "ssh-handoff close: %v\n", err)
		return 2
	}
	if err := closeSession(session); err != nil {
		writeTextf(stderr, "ssh-handoff close: %v\n", err)
		return 2
	}
	writeTextf(stdout, "closed %s\n", session.ID)
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
  --host HOST       直接连接的主机
  --user USER       直接连接的用户
  --port PORT       SSH 端口（默认 22）
  --identity FILE   本地私钥文件
  --profile NAME    OpenSSH profile（仅 Unix）
  --note NOTE       添加用于辨认 session 的备注
  --mode MODE       执行模式：exec（默认）或 shell-pty

进入空闲 Shell 后按 Ctrl-] 切换托管；再次按下恢复交互。
` + platformOpenHelp(), true
	case "run":
		return `通过已有 session 的新 channel 同步执行一条命令。

用法:
  ` + runUsage + `

选项:
  --stream                       以 NDJSON 实时输出
  --timeout DURATION             命令超时时间（默认 1m）
  --shell-ready-delay DURATION   shell-pty 发送命令前等待时间（默认 1s）

session ID 输入不区分大小写。执行模式由 open 时的 --mode 决定。
命令参数为 - 时，从本地标准输入读取完整命令文本。
`, true
	case "list":
		return `列出仍然存活的 session。

用法:
  ` + listUsage + `

结果以表格输出。
`, true
	case "close":
		return `关闭指定 session 及其 SSH transport。

用法:
  ` + closeUsage + `

session ID 输入不区分大小写；该 session 中正在执行的 run 可能随之中断。
`, true
	default:
		return "", false
	}
}

func writeRunSessionError(output runOutput, err error) int {
	if errors.Is(err, errSessionNotFound) {
		return writeRunError(output, "session_not_found", err)
	}
	return writeRunError(output, "local_error", err)
}

func writeRunExecutionError(output runOutput, err error) int {
	if _, ok := errors.AsType[*sessionUnavailableError](err); ok {
		return writeRunError(output, "session_unavailable", err)
	}
	return writeRunError(output, "execution_error", err)
}

func writeRunError(output runOutput, code string, err error) int {
	_ = output.writeError(code, err)
	return 2
}

func writeText(output io.Writer, value string) {
	_, _ = io.WriteString(output, value)
}

func writeTextf(output io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(output, format, args...)
}
