//go:build linux || darwin

package main

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func platformOpenHelp() string {
	return `
当前平台使用系统 OpenSSH。--profile 读取 ~/.ssh/config；直接连接使用
--host、--user、--port 和 --identity。
`
}

func validatePlatformConnection(connectionSpec) error {
	return nil
}

func sshTargetArgs(connection connectionSpec) []string {
	if connection.Profile != "" {
		return []string{"--", connection.Profile}
	}
	arguments := []string{
		"-p", strconv.Itoa(connection.Port),
		"-l", connection.User,
	}
	if connection.Identity != "" {
		arguments = append(arguments, "-i", connection.Identity)
	}
	return append(arguments, "--", connection.Host)
}

func sshCommandContext(ctx context.Context, arguments ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "ssh", arguments...)
	cmd.WaitDelay = childIOWaitDelay
	return cmd
}

func newSessionCommand(
	ctx context.Context,
	session *session,
	remoteCommand string,
) (*exec.Cmd, func(), error) {
	arguments := sshDownstreamArgs(session)
	if session.Mode == modeExec {
		arguments = append(arguments, remoteCommand)
	}
	return sshCommandContext(ctx, arguments...), nil, nil
}

func sshMasterArgs(session *session) []string {
	arguments := []string{"-M", "-S", session.Platform.ControlPath, "-tt"}
	return append(arguments, sshTargetArgs(session.Connection)...)
}

func sshDownstreamArgs(session *session) []string {
	arguments := []string{
		"-S", session.Platform.ControlPath,
		"-o", "ControlMaster=no",
		"-o", "BatchMode=yes",
		"-o", "ProxyCommand=false",
	}
	if session.Mode == modeShellPTY {
		arguments = append(arguments, "-tt")
	}
	return append(arguments, sshTargetArgs(session.Connection)...)
}

func sshControlArgs(session *session, operation string) []string {
	arguments := []string{
		"-S", session.Platform.ControlPath,
		"-O", operation,
		"-o", "BatchMode=yes",
	}
	return append(arguments, sshTargetArgs(session.Connection)...)
}

func runControlCommand(session *session, operation string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return sshCommandContext(ctx, sshControlArgs(session, operation)...).CombinedOutput()
}

func checkSession(session *session) error {
	output, err := runControlCommand(session, "check")
	if err == nil {
		return nil
	}
	message := cmp.Or(strings.TrimSpace(string(output)), err.Error())
	return &sessionUnavailableError{message: fmt.Sprintf("session %s is unavailable: %s", session.ID, message)}
}

func closeSession(session *session) error {
	output, err := runControlCommand(session, "exit")
	if err == nil {
		return nil
	}
	message := cmp.Or(strings.TrimSpace(string(output)), err.Error())
	return fmt.Errorf("session %s did not close: %s", session.ID, message)
}

func terminateProcess(process *os.Process) {
	_ = process.Signal(unix.SIGTERM)
}
