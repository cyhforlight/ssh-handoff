//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	winregistry "golang.org/x/sys/windows/registry"
)

type platformSessionState struct {
	PlinkPath string `json:"plink_path,omitempty"`
	PlinkPID  int    `json:"plink_pid,omitempty"`
}

func newPlatformSessionState(_, _ string) platformSessionState {
	return platformSessionState{}
}

func removePlatformSessionFiles(_, id string) {
	removePlinkProfiles(id)
}

func runtimeDirectory() string {
	base, err := os.UserCacheDir()
	if err == nil && base != "" {
		return filepath.Join(base, "ssh-handoff", "runtime")
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil && home != "" {
		return filepath.Join(home, ".ssh-handoff", "runtime")
	}
	// Do not fall back to a machine-wide temporary directory.
	return ""
}

func ensurePrivateDirectory(path string) error {
	if path == "" {
		return errors.New("cannot determine a runtime directory for the current Windows user")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return validatePrivateDirectory(path)
}

func validatePrivateDirectory(path string) error {
	if path == "" {
		return errors.New("cannot determine a runtime directory for the current Windows user")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("runtime path is not a directory: %s", path)
	}
	return nil
}

func withRegistryLock(dir string, action func() error) error {
	file, err := os.OpenFile(filepath.Join(dir, ".registry.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	var overlapped windows.Overlapped
	handle := windows.Handle(file.Fd())
	if err := windows.LockFileEx(
		handle,
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		1,
		0,
		&overlapped,
	); err != nil {
		return err
	}
	defer func() {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
	}()
	return action()
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // read-only liveness probe
	result, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && result == uint32(windows.WAIT_TIMEOUT)
}

func terminatePID(pid int) error {
	if pid <= 0 {
		return nil
	}
	handle, err := windows.OpenProcess(
		windows.PROCESS_TERMINATE|windows.SYNCHRONIZE,
		false,
		uint32(pid),
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil
		}
		return err
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // best effort during teardown

	result, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return err
	}
	if result == windows.WAIT_OBJECT_0 {
		return nil
	}
	return windows.TerminateProcess(handle, 1)
}

func waitForProcessExit(pid int, timeout time.Duration) bool {
	if pid <= 0 {
		return true
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // read-only liveness probe

	milliseconds := timeout.Milliseconds()
	if milliseconds < 0 {
		milliseconds = 0
	}
	if milliseconds > int64(^uint32(0)-1) {
		milliseconds = int64(^uint32(0) - 1)
	}
	result, err := windows.WaitForSingleObject(handle, uint32(milliseconds))
	return err == nil && result == windows.WAIT_OBJECT_0
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
