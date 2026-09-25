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

func sshSessionArgs(session *session) []string {
	return []string{
		"-S", session.Platform.ControlPath,
		"-o", "ControlPersist=no",
		"-o", "ForkAfterAuthentication=no",
		"-o", "StdinNull=no",
	}
}

func sshMasterArgs(session *session) []string {
	arguments := append(sshSessionArgs(session), "-M", "-tt")
	return append(arguments, sshTargetArgs(session.Connection)...)
}

func sshDownstreamArgs(session *session) []string {
	arguments := append(sshSessionArgs(session),
		"-o", "ControlMaster=no",
		"-o", "BatchMode=yes",
		// ControlMaster=no reuses the master, but falls back to a normal direct
		// connection when the control socket is missing or not listening. That
		// fallback would authenticate independently and bypass session
		// ownership, so ProxyCommand=false makes it fail instead. RemoteCommand
		// has no documented disabling value: "none" runs a command by that name
		// and keeps the configured RemoteCommand off the downstream channel.
		"-o", "ProxyCommand=false",
		"-o", "RemoteCommand=none",
	)
	if session.Mode == modeShellPTY {
		arguments = append(arguments, "-tt")
	} else {
		arguments = append(arguments, "-T")
	}
	return append(arguments, sshTargetArgs(session.Connection)...)
}

func sshControlArgs(session *session, operation string) []string {
	arguments := append(sshSessionArgs(session),
		"-O", operation,
		"-o", "BatchMode=yes",
	)
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
