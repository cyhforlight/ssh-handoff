//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsRuntimeDirectoryAndRegistryLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime")
	if err := ensurePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	if err := withRegistryLock(path, func() error {
		return os.WriteFile(filepath.Join(path, "inside"), []byte("locked"), 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	if !processAlive(os.Getpid()) {
		t.Fatal("current process is not reported alive")
	}
	if processAlive(-1) {
		t.Fatal("negative process ID is reported alive")
	}
}

func TestWindowsRuntimeDirectoryFallbackUsesUserProfile(t *testing.T) {
	t.Setenv("LocalAppData", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".ssh-handoff", "runtime")
	if got := runtimeDirectory(); got != want {
		t.Fatalf("runtimeDirectory() = %q, want %q", got, want)
	}
}
