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
	if len(filepath.Join(dir, "0123456789abcdef.sock")) > maximumControlPath {
		dir = filepath.Join("/tmp", fmt.Sprintf("ssh-handoff-%d", os.Getuid()))
	}
	return dir
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runtime path is not a directory: %s", path)
	}
	stat, ok := info.Sys().(*unix.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("runtime directory is not owned by the current user: %s", path)
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(path, 0o700); err != nil {
			return err
		}
	}
	return nil
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
	defer unix.Flock(int(file.Fd()), unix.LOCK_UN) //nolint:errcheck // lock is released on close regardless
	return action()
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}
