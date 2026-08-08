//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

type platformSessionState struct {
	PlinkPath         string `json:"plink_path,omitzero"`
	PlinkPID          int    `json:"plink_pid,omitzero"`
	PlinkCreationTime uint64 `json:"plink_creation_time,omitzero"`
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

func processCreationTime(handle windows.Handle) (uint64, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return 0, err
	}
	return uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime), nil
}

func terminatePlinkProcess(pid int, creationTime uint64) error {
	if pid <= 0 || creationTime == 0 {
		return fmt.Errorf("missing process identity for PID %d", pid)
	}
	handle, err := windows.OpenProcess(
		windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE,
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
	actualCreationTime, err := processCreationTime(handle)
	if err != nil {
		return err
	}
	if actualCreationTime != creationTime {
		return fmt.Errorf("process identity does not match session: PID %d", pid)
	}

	result, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return err
	}
	if result == windows.WAIT_OBJECT_0 {
		return nil
	}
	if err := windows.TerminateProcess(handle, 1); err != nil {
		return err
	}
	result, err = windows.WaitForSingleObject(handle, uint32((3 * time.Second).Milliseconds()))
	if err != nil {
		return err
	}
	if result != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("process %d did not exit", pid)
	}
	return nil
}
