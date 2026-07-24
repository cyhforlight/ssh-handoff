//go:build linux || darwin

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const maximumControlPath = 96

type platformSessionState struct {
	ControlPath string `json:"control_path"`
}

func newPlatformSessionState(dir, id string) platformSessionState {
	return platformSessionState{ControlPath: filepath.Join(dir, id+".sock")}
}

func removePlatformSessionFiles(dir, id string) {
	_ = os.Remove(filepath.Join(dir, id+".sock"))
}

func runtimeDirectory() string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, fmt.Sprintf("ssh-handoff-%d", os.Getuid()))
	if len(filepath.Join(dir, "ABCD.sock")) > maximumControlPath {
		dir = filepath.Join("/tmp", fmt.Sprintf("ssh-handoff-%d", os.Getuid()))
	}
	return dir
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	permissions, err := privateDirectoryPermissions(path)
	if err != nil {
		return err
	}
	if permissions != 0o700 {
		return os.Chmod(path, 0o700)
	}
	return nil
}

func validatePrivateDirectory(path string) error {
	permissions, err := privateDirectoryPermissions(path)
	if err != nil {
		return err
	}
	if permissions != 0o700 {
		return fmt.Errorf("runtime directory permissions are %o, want 700: %s", permissions, path)
	}
	return nil
}

func privateDirectoryPermissions(path string) (uint32, error) {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return 0, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return 0, fmt.Errorf("runtime path is not a directory: %s", path)
	}
	if stat.Uid != uint32(os.Getuid()) {
		return 0, fmt.Errorf("runtime directory is not owned by the current user: %s", path)
	}
	return uint32(stat.Mode & 0o777), nil
}

func withFileLock(path string, action func() error) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	return action()
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}
