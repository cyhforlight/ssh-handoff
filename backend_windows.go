//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var errInvalidSSHCommand = errors.New(
	"Windows Plink backend requires: ssh [-4|-6] [-C] [-i FILE] [-l USER] [-p PORT] USER@HOST",
)

const childIOWaitDelay = 100 * time.Millisecond

type plinkTarget struct {
	Host         string
	User         string
	Port         int
	AddressFlag  string
	Compression  bool
	IdentityFile string
}

func platformOpenHelp() string {
	return `
Windows 使用 Plink 与 ConPTY；连接命令必须显式指定用户，只支持
-4、-6、-C、-i FILE、-l USER 和 -p PORT。Plink 路径可通过
SSH_HANDOFF_PLINK 指定。
`
}

func validateOpenCommand(command string) error {
	_, err := parsePlinkTarget(command)
	return err
}

func parsePlinkTarget(command string) (plinkTarget, error) {
	arguments, err := splitSSHCommand(command)
	if err != nil {
		return plinkTarget{}, err
	}
	if len(arguments) < 2 || arguments[0] != "ssh" {
		return plinkTarget{}, errInvalidSSHCommand
	}

	target := plinkTarget{Port: 22}
	var destinationUser string
	var optionUser string
	haveDestination := false
	options := true

	for index := 1; index < len(arguments); index++ {
		argument := arguments[index]
		if options && argument == "--" {
			options = false
			continue
		}
		if options && strings.HasPrefix(argument, "-") && argument != "-" {
			switch {
			case argument == "-4" || argument == "-6":
				if target.AddressFlag != "" && target.AddressFlag != argument {
					return plinkTarget{}, errors.New("SSH command cannot select both IPv4 and IPv6")
				}
				target.AddressFlag = argument
			case argument == "-C":
				target.Compression = true
			case argument == "-p" || argument == "-l" || argument == "-i":
				index++
				if index >= len(arguments) {
					return plinkTarget{}, fmt.Errorf("SSH option %s requires a value", argument)
				}
				if err := applyPlinkOption(&target, &optionUser, argument, arguments[index]); err != nil {
					return plinkTarget{}, err
				}
			case len(argument) > 2 && (strings.HasPrefix(argument, "-p") ||
				strings.HasPrefix(argument, "-l") ||
				strings.HasPrefix(argument, "-i")):
				if err := applyPlinkOption(&target, &optionUser, argument[:2], argument[2:]); err != nil {
					return plinkTarget{}, err
				}
			default:
				return plinkTarget{}, fmt.Errorf(
					"SSH option %q is not supported by the Windows Plink backend",
					argument,
				)
			}
			continue
		}

		if haveDestination {
			return plinkTarget{}, errors.New("SSH login command must not include a remote command")
		}
		var err error
		destinationUser, target.Host, err = parseSSHDestination(argument)
		if err != nil {
			return plinkTarget{}, err
		}
		haveDestination = true
	}

	if !haveDestination {
		return plinkTarget{}, errInvalidSSHCommand
	}
	if optionUser != "" && destinationUser != "" && optionUser != destinationUser {
		return plinkTarget{}, fmt.Errorf(
			"SSH command specifies conflicting users %q and %q",
			destinationUser,
			optionUser,
		)
	}
	target.User = optionUser
	if target.User == "" {
		target.User = destinationUser
	}
	if target.User == "" {
		return plinkTarget{}, errors.New(
			"Windows Plink backend requires an explicit SSH user (-l USER or USER@HOST)",
		)
	}
	return target, nil
}

func applyPlinkOption(target *plinkTarget, user *string, option, value string) error {
	if value == "" {
		return fmt.Errorf("SSH option %s requires a non-empty value", option)
	}
	switch option {
	case "-p":
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("invalid SSH port %q", value)
		}
		target.Port = port
	case "-l":
		*user = value
	case "-i":
		target.IdentityFile = value
	}
	return nil
}

func parseSSHDestination(value string) (string, string, error) {
	if value == "" || strings.HasPrefix(value, "-") {
		return "", "", errInvalidSSHCommand
	}
	user := ""
	host := value
	if separator := strings.LastIndexByte(value, '@'); separator >= 0 {
		user = value[:separator]
		host = value[separator+1:]
		if user == "" {
			return "", "", errors.New("SSH destination user must not be empty")
		}
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if host == "" || strings.ContainsAny(host, " \t\r\n\x00") {
		return "", "", errors.New("SSH destination host must not be empty")
	}
	return user, host, nil
}

func splitSSHCommand(command string) ([]string, error) {
	if strings.ContainsAny(command, "\r\n\x00") {
		return nil, errors.New("SSH command must be a single line without NUL")
	}

	var arguments []string
	var token strings.Builder
	started := false
	quote := byte(0)
	escaped := false
	flush := func() {
		if !started {
			return
		}
		arguments = append(arguments, token.String())
		token.Reset()
		started = false
	}

	for index := 0; index < len(command); index++ {
		character := command[index]
		if escaped {
			token.WriteByte(character)
			started = true
			escaped = false
			continue
		}
		switch quote {
		case '\'':
			if character == '\'' {
				quote = 0
			} else {
				token.WriteByte(character)
			}
			started = true
		case '"':
			switch character {
			case '"':
				quote = 0
				started = true
			case '\\':
				started = true
				if index+1 < len(command) && command[index+1] == '"' {
					escaped = true
				} else {
					token.WriteByte(character)
				}
			default:
				token.WriteByte(character)
				started = true
			}
		default:
			switch character {
			case ' ', '\t':
				flush()
			case '\'', '"':
				quote = character
				started = true
			case '\\':
				started = true
				if index+1 < len(command) && strings.ContainsRune(
					" \t'\"",
					rune(command[index+1]),
				) {
					escaped = true
				} else {
					token.WriteByte(character)
				}
			default:
				token.WriteByte(character)
				started = true
			}
		}
	}
	if escaped {
		return nil, errors.New("SSH command ends with an incomplete escape")
	}
	if quote != 0 {
		return nil, errors.New("SSH command contains an unclosed quote")
	}
	flush()
	return arguments, nil
}

func resolvePlinkPath() (string, error) {
	if configured := os.Getenv("SSH_HANDOFF_PLINK"); configured != "" {
		return validatePlinkPath(configured)
	}

	executable, err := os.Executable()
	if err == nil {
		base := filepath.Dir(executable)
		for _, candidate := range []string{
			filepath.Join(base, "plink.exe"),
			filepath.Join(base, "bin", "plink.exe"),
		} {
			if path, err := validatePlinkPath(candidate); err == nil {
				return path, nil
			}
		}
	}
	path, err := exec.LookPath("plink.exe")
	if err != nil {
		return "", errors.New(
			"plink.exe not found; place it next to ssh-handoff.exe, in bin, or on PATH",
		)
	}
	return filepath.Abs(path)
}

func validatePlinkPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("plink.exe not found: %s", absolute)
	}
	if info.IsDir() {
		return "", fmt.Errorf("plink.exe path is a directory: %s", absolute)
	}
	return absolute, nil
}

func plinkTargetArgs(target plinkTarget, includeIdentity bool) []string {
	arguments := []string{"-P", strconv.Itoa(target.Port), "-l", target.User}
	if target.AddressFlag != "" {
		arguments = append(arguments, target.AddressFlag)
	}
	if target.Compression {
		arguments = append(arguments, "-C")
	}
	if includeIdentity && target.IdentityFile != "" {
		arguments = append(arguments, "-i", target.IdentityFile)
	}
	return append(arguments, target.Host)
}

func plinkMasterArgs(session *session, target plinkTarget) []string {
	arguments := []string{
		"-load", plinkProfileName(session.ID, "upstream"),
		"-ssh",
		"-share",
		"-t",
	}
	return append(arguments, plinkTargetArgs(target, true)...)
}

func plinkDownstreamArgs(session *session, target plinkTarget, commandFile string) []string {
	arguments := []string{
		"-load", plinkProfileName(session.ID, "downstream"),
		"-ssh",
		"-share",
		"-batch",
		"-no-antispoof",
		"-noagent",
		"-no-trivial-auth",
		"-no-sanitise-stdout",
		"-no-sanitise-stderr",
	}
	if session.Mode == modeShellPTY {
		arguments = append(arguments, "-t")
	} else {
		arguments = append(arguments, "-T")
		if commandFile != "" {
			arguments = append(arguments, "-m", commandFile)
		}
	}
	// A sharing downstream does not authenticate. In particular, do not pass
	// the master's private key: if the upstream disappears between the
	// shareexists check and process startup, Plink otherwise falls back to a
	// fresh SSH connection and might authenticate independently.
	return append(arguments, plinkTargetArgs(target, false)...)
}

func writePlinkCommandFile(dir, remoteCommand string) (string, error) {
	file, err := os.CreateTemp(dir, "command-*.txt")
	if err != nil {
		return "", err
	}
	path := file.Name()
	removeOnError := func() {
		_ = file.Close()
		_ = os.Remove(path)
	}
	if _, err := file.WriteString(remoteCommand); err != nil {
		removeOnError()
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func plinkShareExistsArgs(session *session, target plinkTarget) []string {
	arguments := []string{
		"-load", plinkProfileName(session.ID, "downstream"),
		"-ssh",
		"-shareexists",
	}
	return append(arguments, plinkTargetArgs(target, false)...)
}

func plinkCommandContext(ctx context.Context, path string, arguments ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, arguments...)
	cmd.WaitDelay = childIOWaitDelay
	return cmd
}

func plinkShareExists(
	ctx context.Context,
	session *session,
	target plinkTarget,
) (bool, string, error) {
	cmd := plinkCommandContext(
		ctx,
		session.Platform.PlinkPath,
		plinkShareExistsArgs(session, target)...,
	)
	output, err := cmd.CombinedOutput()
	message := strings.TrimSpace(string(output))
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return false, message, errors.New("shared Plink connection check timed out")
	}
	if err == nil {
		return true, message, nil
	}
	if _, ok := errors.AsType[*exec.ExitError](err); ok {
		return false, message, nil
	}
	return false, message, err
}

func waitForPlinkShare(session *session, target plinkTarget, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var message string
	for {
		exists, currentMessage, err := plinkShareExists(ctx, session, target)
		if currentMessage != "" {
			message = currentMessage
		}
		if exists {
			return nil
		}
		if err != nil {
			if message != "" {
				return fmt.Errorf("%w: %s", err, message)
			}
			return err
		}
		if !processAlive(session.Platform.PlinkPID) {
			if message == "" {
				message = "Plink master exited before publishing its shared connection"
			}
			return errors.New(message)
		}

		select {
		case <-ctx.Done():
			if message == "" {
				message = "shared Plink connection did not become available"
			}
			return fmt.Errorf("%s: %w", message, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
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
	target, err := parsePlinkTarget(session.Command)
	if err != nil {
		return runStatus{}, err
	}
	if err := checkSessionTarget(session, target); err != nil {
		return runStatus{}, err
	}

	status := runStatus{Session: session.ID, Mode: session.Mode}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	commandFile := ""
	if session.Mode == modeExec {
		commandFile, err = writePlinkCommandFile(runtimeDirectory(), remoteCommand)
		if err != nil {
			return runStatus{}, fmt.Errorf("create Plink command file: %w", err)
		}
		defer func() { _ = os.Remove(commandFile) }()
	}
	arguments := plinkDownstreamArgs(session, target, commandFile)
	cmd := plinkCommandContext(ctx, session.Platform.PlinkPath, arguments...)
	streams := newSerializedOutput(emit)
	if session.Mode == modeShellPTY {
		cmd.Stdin = strings.NewReader(strings.TrimRight(remoteCommand, "\n") + "\nexit\n")
		writer := streams.writer(streamOutput)
		cmd.Stdout, cmd.Stderr = writer, writer
	} else {
		cmd.Stdout = streams.writer(streamStdout)
		cmd.Stderr = streams.writer(streamStderr)
	}
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
	target, err := parsePlinkTarget(session.Command)
	if err != nil {
		return err
	}
	return checkSessionTarget(session, target)
}

func checkSessionTarget(session *session, target plinkTarget) error {
	if session.Platform.PlinkPath == "" || !processAlive(session.Platform.PlinkPID) {
		return &sessionUnavailableError{
			message: fmt.Sprintf("session %s is unavailable: Plink master is not running", session.ID),
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exists, message, err := plinkShareExists(ctx, session, target)
	if err != nil {
		return &sessionUnavailableError{
			message: fmt.Sprintf("session %s is unavailable: %v", session.ID, err),
		}
	}
	if exists {
		return nil
	}
	if message == "" {
		message = "shared Plink connection was not found"
	}
	return &sessionUnavailableError{
		message: fmt.Sprintf("session %s is unavailable: %s", session.ID, message),
	}
}

func closeSession(session *session) error {
	if session.Platform.PlinkPID > 0 {
		if err := terminatePID(session.Platform.PlinkPID); err != nil {
			return fmt.Errorf("close Plink master: %w", err)
		}
	}
	if waitForProcessExit(session.PID, 3*time.Second) {
		return nil
	}
	if err := terminatePID(session.PID); err != nil {
		return err
	}
	if !waitForProcessExit(session.PID, time.Second) {
		return fmt.Errorf("session %s did not close", session.ID)
	}
	return nil
}
