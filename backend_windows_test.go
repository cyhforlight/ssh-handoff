//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	winregistry "golang.org/x/sys/windows/registry"
)

func TestWindowsRejectsOpenSSHProfile(t *testing.T) {
	registry := &sessionRegistry{dir: filepath.Join(t.TempDir(), "runtime")}
	_, err := registry.create("", modeExec, connectionSpec{Profile: "myserver"})
	if err == nil || !strings.Contains(err.Error(), "does not support OpenSSH profiles") {
		t.Fatalf("registry.create() error = %v", err)
	}
}

func TestPlinkProfilesSeparateUpstreamAndDownstream(t *testing.T) {
	id := fmt.Sprintf("TEST%d", os.Getpid())
	removePlinkProfiles(id)
	t.Cleanup(func() { removePlinkProfiles(id) })

	if err := createPlinkProfiles(id); err != nil {
		t.Fatal(err)
	}
	upstream := plinkProfileName(id, plinkProfileUpstream)
	downstream := plinkProfileName(id, plinkProfileDownstream)
	assertProfileValue(
		t,
		upstream,
		plinkProfileOwnerValue,
		1,
	)
	assertProfileValue(
		t,
		upstream,
		"ConnectionSharingUpstream",
		1,
	)
	assertProfileValue(
		t,
		upstream,
		"ConnectionSharingDownstream",
		0,
	)
	assertProfileValue(
		t,
		downstream,
		"ConnectionSharingUpstream",
		0,
	)
	assertProfileValue(
		t,
		downstream,
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
		assertProfileValue(t, downstream, value, 0)
	}
	assertProfileValue(t, downstream, "SshNoTrivialAuth", 1)
	for _, value := range []string{
		"PublicKeyFile",
		"DetachedCertificate",
		"AuthPlugin",
	} {
		assertProfileString(t, downstream, value, "")
	}

	removePlinkProfiles(id)
	if key, err := winregistry.OpenKey(
		winregistry.CURRENT_USER,
		puttySessionsRegistryPath+`\`+downstream,
		winregistry.QUERY_VALUE,
	); err == nil {
		_ = key.Close()
		t.Fatal("removePlinkProfiles left the downstream profile behind")
	}
}

func TestRemovePlinkProfilePreservesUnmanagedSavedSession(t *testing.T) {
	id := fmt.Sprintf("unmanaged-%d-%d", os.Getpid(), time.Now().UnixNano())
	name := plinkProfileName(id, plinkProfileUpstream)
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
	if err := writePlinkProfile(id, plinkProfileUpstream); err == nil {
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

func TestPlinkArgumentsPreserveExecutionMode(t *testing.T) {
	assertArgs := func(name string, got, want []string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Fatalf("%s = %#v, want %#v", name, got, want)
		}
	}
	session := &session{
		ID: "A3B4",
		Connection: connectionSpec{
			Host:     "2001:db8::10",
			User:     "JMS-token",
			Port:     2222,
			Identity: `C:\Keys\operator.ppk`,
		},
		Mode: modeExec,
	}

	wantMaster := []string{
		"-load", "ssh-handoff-A3B4-upstream", "-ssh", "-share", "-no-antispoof", "-t",
		"-P", "2222", "-l", "JMS-token", "-i", `C:\Keys\operator.ppk`,
		"2001:db8::10",
	}
	assertArgs("plinkMasterArgs()", plinkMasterArgs(session), wantMaster)

	commandFile := `C:\Temp\ssh-handoff-command.txt`
	wantExec := []string{
		"-load", "ssh-handoff-A3B4-downstream", "-ssh", "-share", "-batch",
		"-no-antispoof", "-noagent", "-no-trivial-auth",
		"-no-sanitise-stdout", "-no-sanitise-stderr",
		"-T", "-m", commandFile,
		"-P", "2222", "-l", "JMS-token", "2001:db8::10",
	}
	assertArgs("plinkDownstreamArgs()", plinkDownstreamArgs(session, commandFile), wantExec)

	session.Mode = modeShellPTY
	wantPTY := []string{
		"-load", "ssh-handoff-A3B4-downstream", "-ssh", "-share", "-batch",
		"-no-antispoof", "-noagent", "-no-trivial-auth",
		"-no-sanitise-stdout", "-no-sanitise-stderr",
		"-t", "-P", "2222", "-l", "JMS-token", "2001:db8::10",
	}
	assertArgs("plinkDownstreamArgs()", plinkDownstreamArgs(session, ""), wantPTY)

	wantShareExists := []string{
		"-load", "ssh-handoff-A3B4-downstream", "-ssh", "-shareexists",
		"-P", "2222", "-l", "JMS-token", "2001:db8::10",
	}
	assertArgs("plinkShareExistsArgs()", plinkShareExistsArgs(session), wantShareExists)
}

func TestWritePlinkCommandFilePreservesScript(t *testing.T) {
	script := "printf '你好\\n'\n" + strings.Repeat("echo payload\n", 4000)
	path, err := writePlinkCommandFile(t.TempDir(), script)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != script {
		t.Fatal("Plink command file did not preserve the script")
	}
}

func TestResolveConfiguredPlinkPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plink.exe")
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_HANDOFF_PLINK", path)

	got, err := resolvePlinkPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("resolvePlinkPath() = %q, want %q", got, path)
	}
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
