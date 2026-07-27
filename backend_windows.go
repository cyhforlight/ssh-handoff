//go:build windows

package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	winregistry "golang.org/x/sys/windows/registry"
)

func platformOpenHelp() string {
	return `
Windows 使用 Plink 与 ConPTY，支持 --host、--user、--port 和
--identity 直接连接，不支持 OpenSSH profile。Plink 路径可通过
SSH_HANDOFF_PLINK 指定。
`
}

func validatePlatformConnection(connection connectionSpec) error {
	if connection.Profile != "" {
		return errors.New("the Windows Plink backend does not support OpenSSH profiles")
	}
	return nil
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

type plinkProfileRole string

const (
	puttySessionsRegistryPath                  = `Software\SimonTatham\PuTTY\Sessions`
	plinkProfileOwnerValue                     = "SSH-HandoffManaged"
	plinkProfileUpstream      plinkProfileRole = "upstream"
	plinkProfileDownstream    plinkProfileRole = "downstream"
)

func plinkProfileName(id string, role plinkProfileRole) string {
	return "ssh-handoff-" + id + "-" + string(role)
}

func createPlinkProfiles(id string) error {
	if err := writePlinkProfile(id, plinkProfileUpstream); err != nil {
		return err
	}
	if err := writePlinkProfile(id, plinkProfileDownstream); err != nil {
		removePlinkProfile(plinkProfileName(id, plinkProfileUpstream))
		return err
	}
	return nil
}

func writePlinkProfile(id string, role plinkProfileRole) error {
	name := plinkProfileName(id, role)
	removePlinkProfile(name)
	path := puttySessionsRegistryPath + `\` + name
	key, openedExisting, err := winregistry.CreateKey(
		winregistry.CURRENT_USER,
		path,
		winregistry.SET_VALUE,
	)
	if err != nil {
		return fmt.Errorf("create temporary PuTTY session %s: %w", name, err)
	}
	keyOpen := true
	defer func() {
		if keyOpen {
			_ = key.Close()
		}
	}()
	if openedExisting {
		return fmt.Errorf(
			"temporary PuTTY session name conflicts with an unmanaged saved session: %s",
			name,
		)
	}
	closeAndDelete := func() {
		_ = key.Close()
		keyOpen = false
		deletePlinkProfile(name)
	}
	if err := key.SetDWordValue(plinkProfileOwnerValue, 1); err != nil {
		closeAndDelete()
		return fmt.Errorf("mark temporary PuTTY session %s: %w", name, err)
	}

	upstreamValue, downstreamValue := uint32(0), uint32(1)
	if role == plinkProfileUpstream {
		upstreamValue, downstreamValue = 1, 0
	}
	values := map[string]uint32{
		"ConnectionSharing":           1,
		"ConnectionSharingUpstream":   upstreamValue,
		"ConnectionSharingDownstream": downstreamValue,
	}
	stringValues := map[string]string{
		"Protocol": "ssh",
	}
	if role == plinkProfileDownstream {
		values["TryAgent"] = 0
		values["AgentFwd"] = 0
		values["AuthGSSAPI"] = 0
		values["AuthGSSAPIKEX"] = 0
		values["AuthTIS"] = 0
		values["AuthKI"] = 0
		values["SshNoTrivialAuth"] = 1
		stringValues["PublicKeyFile"] = ""
		stringValues["DetachedCertificate"] = ""
		stringValues["AuthPlugin"] = ""
	}
	for value, data := range values {
		if err := key.SetDWordValue(value, data); err != nil {
			closeAndDelete()
			return fmt.Errorf("configure temporary PuTTY session %s: %w", name, err)
		}
	}
	for value, data := range stringValues {
		if err := key.SetStringValue(value, data); err != nil {
			closeAndDelete()
			return fmt.Errorf("configure temporary PuTTY session %s: %w", name, err)
		}
	}
	return nil
}

func removePlinkProfiles(id string) {
	removePlinkProfile(plinkProfileName(id, plinkProfileUpstream))
	removePlinkProfile(plinkProfileName(id, plinkProfileDownstream))
}

func removePlinkProfile(name string) {
	path := puttySessionsRegistryPath + `\` + name
	key, err := winregistry.OpenKey(
		winregistry.CURRENT_USER,
		path,
		winregistry.QUERY_VALUE,
	)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return
	}
	if err != nil {
		return
	}
	managed, _, markerErr := key.GetIntegerValue(plinkProfileOwnerValue)
	_ = key.Close()
	if markerErr != nil || managed != 1 {
		return
	}
	deletePlinkProfile(name)
}

func deletePlinkProfile(name string) {
	_ = winregistry.DeleteKey(
		winregistry.CURRENT_USER,
		puttySessionsRegistryPath+`\`+name,
	)
}

func plinkTargetArgs(connection connectionSpec, includeIdentity bool) []string {
	arguments := []string{"-P", strconv.Itoa(connection.Port), "-l", connection.User}
	if includeIdentity && connection.Identity != "" {
		arguments = append(arguments, "-i", connection.Identity)
	}
	return append(arguments, connection.Host)
}

func plinkMasterArgs(session *session) []string {
	arguments := []string{
		"-load", plinkProfileName(session.ID, plinkProfileUpstream),
		"-ssh",
		"-share",
		"-no-antispoof",
		"-t",
	}
	return append(arguments, plinkTargetArgs(session.Connection, true)...)
}

func plinkDownstreamArgs(session *session, commandFile string) []string {
	arguments := []string{
		"-load", plinkProfileName(session.ID, plinkProfileDownstream),
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
	return append(arguments, plinkTargetArgs(session.Connection, false)...)
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

func plinkShareExistsArgs(session *session) []string {
	arguments := []string{
		"-load", plinkProfileName(session.ID, plinkProfileDownstream),
		"-ssh",
		"-shareexists",
	}
	return append(arguments, plinkTargetArgs(session.Connection, false)...)
}

func plinkCommandContext(ctx context.Context, path string, arguments ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, arguments...)
	cmd.WaitDelay = childIOWaitDelay
	return cmd
}

func newSessionCommand(
	ctx context.Context,
	session *session,
	remoteCommand string,
) (*exec.Cmd, func(), error) {
	commandFile := ""
	if session.Mode == modeExec {
		var err error
		commandFile, err = writePlinkCommandFile(runtimeDirectory(), remoteCommand)
		if err != nil {
			return nil, nil, fmt.Errorf("create Plink command file: %w", err)
		}
	}
	arguments := plinkDownstreamArgs(session, commandFile)
	cmd := plinkCommandContext(ctx, session.Platform.PlinkPath, arguments...)
	if commandFile == "" {
		return cmd, nil, nil
	}
	return cmd, func() { _ = os.Remove(commandFile) }, nil
}

func plinkShareExists(
	ctx context.Context,
	session *session,
) (bool, string, error) {
	cmd := plinkCommandContext(
		ctx,
		session.Platform.PlinkPath,
		plinkShareExistsArgs(session)...,
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

func waitForPlinkShare(session *session, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var message string
	for {
		exists, currentMessage, err := plinkShareExists(ctx, session)
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
			message = cmp.Or(message, "Plink master exited before publishing its shared connection")
			return errors.New(message)
		}

		select {
		case <-ctx.Done():
			message = cmp.Or(message, "shared Plink connection did not become available")
			return fmt.Errorf("%s: %w", message, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func checkSession(session *session) error {
	if session.Platform.PlinkPath == "" || !processAlive(session.Platform.PlinkPID) {
		return &sessionUnavailableError{
			message: fmt.Sprintf("session %s is unavailable: Plink master is not running", session.ID),
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exists, message, err := plinkShareExists(ctx, session)
	if err != nil {
		return &sessionUnavailableError{
			message: fmt.Sprintf("session %s is unavailable: %v", session.ID, err),
		}
	}
	if exists {
		return nil
	}
	message = cmp.Or(message, "shared Plink connection was not found")
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
