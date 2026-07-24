//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	winregistry "golang.org/x/sys/windows/registry"
)

func TestWindowsRuntimeDirectoryAndFileLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime")
	if err := ensurePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	if err := withFileLock(filepath.Join(path, "session.lock"), func() error {
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

func TestPlinkProfilesSeparateUpstreamAndDownstream(t *testing.T) {
	id := fmt.Sprintf("TEST%d", os.Getpid())
	removePlinkProfiles(id)
	t.Cleanup(func() { removePlinkProfiles(id) })

	if err := createPlinkProfiles(id); err != nil {
		t.Fatal(err)
	}
	assertProfileValue(
		t,
		plinkProfileName(id, "upstream"),
		plinkProfileOwnerValue,
		1,
	)
	assertProfileValue(
		t,
		plinkProfileName(id, "upstream"),
		"ConnectionSharingUpstream",
		1,
	)
	assertProfileValue(
		t,
		plinkProfileName(id, "upstream"),
		"ConnectionSharingDownstream",
		0,
	)
	assertProfileValue(
		t,
		plinkProfileName(id, "downstream"),
		"ConnectionSharingUpstream",
		0,
	)
	assertProfileValue(
		t,
		plinkProfileName(id, "downstream"),
		"ConnectionSharingDownstream",
		1,
	)
	for _, value := range []string{
		"TryAgent",
		"AgentFwd",
		"AuthGSSAPI",
		"AuthGSSAPIKEX",
		"AuthTIS",
		"AuthKI",
	} {
		assertProfileValue(t, plinkProfileName(id, "downstream"), value, 0)
	}
	assertProfileValue(t, plinkProfileName(id, "downstream"), "SshNoTrivialAuth", 1)
	for _, value := range []string{
		"PublicKeyFile",
		"DetachedCertificate",
		"AuthPlugin",
	} {
		assertProfileString(t, plinkProfileName(id, "downstream"), value, "")
	}

	removePlinkProfiles(id)
	if key, err := winregistry.OpenKey(
		winregistry.CURRENT_USER,
		puttySessionsRegistryPath+`\`+plinkProfileName(id, "downstream"),
		winregistry.QUERY_VALUE,
	); err == nil {
		_ = key.Close()
		t.Fatal("removePlinkProfiles left the downstream profile behind")
	}
}

func TestRemovePlinkProfilePreservesUnmanagedSavedSession(t *testing.T) {
	name := fmt.Sprintf("ssh-handoff-unmanaged-%d-%d", os.Getpid(), time.Now().UnixNano())
	path := puttySessionsRegistryPath + `\` + name

	key, openedExisting, err := winregistry.CreateKey(
		winregistry.CURRENT_USER,
		path,
		winregistry.SET_VALUE,
	)
	if err != nil {
		t.Fatal(err)
	}
	if openedExisting {
		_ = key.Close()
		t.Fatalf("unexpected pre-existing test profile: %s", name)
	}
	t.Cleanup(func() { deletePlinkProfile(name) })
	if err := key.SetStringValue("HostName", "do-not-delete.example.com"); err != nil {
		_ = key.Close()
		t.Fatal(err)
	}
	_ = key.Close()

	removePlinkProfile(name)
	if err := writePlinkProfile(name, true, false, false); err == nil {
		t.Fatal("writePlinkProfile overwrote an unmanaged saved session")
	}
	key, err = winregistry.OpenKey(
		winregistry.CURRENT_USER,
		path,
		winregistry.QUERY_VALUE,
	)
	if err != nil {
		t.Fatalf("removePlinkProfile deleted an unmanaged saved session: %v", err)
	}
	host, _, err := key.GetStringValue("HostName")
	if err != nil {
		_ = key.Close()
		t.Fatal(err)
	}
	if host != "do-not-delete.example.com" {
		_ = key.Close()
		t.Fatalf("unmanaged saved session was modified: HostName=%q", host)
	}
	_ = key.Close()
}

func assertProfileValue(t *testing.T, profile, value string, want uint64) {
	t.Helper()
	key, err := winregistry.OpenKey(
		winregistry.CURRENT_USER,
		puttySessionsRegistryPath+`\`+profile,
		winregistry.QUERY_VALUE,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = key.Close() }()
	got, _, err := key.GetIntegerValue(value)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s/%s = %d, want %d", profile, value, got, want)
	}
}

func assertProfileString(t *testing.T, profile, value, want string) {
	t.Helper()
	key, err := winregistry.OpenKey(
		winregistry.CURRENT_USER,
		puttySessionsRegistryPath+`\`+profile,
		winregistry.QUERY_VALUE,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = key.Close() }()
	got, _, err := key.GetStringValue(value)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s/%s = %q, want %q", profile, value, got, want)
	}
}
