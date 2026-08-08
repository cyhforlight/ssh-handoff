//go:build windows

package main

import (
	"cmp"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
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

func TestTerminatePlinkProcessVerifiesIdentity(t *testing.T) {
	path := cmp.Or(os.Getenv("ComSpec"), `C:\Windows\System32\cmd.exe`)
	cmd := exec.Command(path, "/d", "/c", "ping -n 30 127.0.0.1 >nul")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	handle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		t.Fatal(err)
	}
	createdAt, err := processCreationTime(handle)
	_ = windows.CloseHandle(handle)
	if err != nil {
		t.Fatal(err)
	}

	err = terminatePlinkProcess(cmd.Process.Pid, createdAt+1)
	if err == nil {
		t.Fatal("terminatePlinkProcess() unexpectedly accepted a different process identity")
	}
	if !processAlive(cmd.Process.Pid) {
		t.Fatal("terminatePlinkProcess() terminated a process with a different identity")
	}
	if err := terminatePlinkProcess(cmd.Process.Pid, createdAt); err != nil {
		t.Fatal(err)
	}
	waitErr := cmd.Wait()
	waited = true
	if waitErr == nil {
		t.Fatal("terminated process reported success")
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
