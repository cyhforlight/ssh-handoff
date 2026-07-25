//go:build windows

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWindowsRejectsOpenSSHProfile(t *testing.T) {
	registry := &sessionRegistry{dir: filepath.Join(t.TempDir(), "runtime")}
	_, err := registry.create("", modeExec, connectionSpec{Profile: "myserver"})
	if err == nil || !strings.Contains(err.Error(), "does not support OpenSSH profiles") {
		t.Fatalf("registry.create() error = %v", err)
	}
}

func TestPlinkArgumentsPreserveExecutionMode(t *testing.T) {
	assertArgs := func(name string, got, want []string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Fatalf("%s = %#v, want %#v", name, got, want)
		}
	}
	session := &session{
		sessionInfo: sessionInfo{
			ID: "A3B4",
			Connection: connectionSpec{
				Host:     "2001:db8::10",
				User:     "JMS-token",
				Port:     2222,
				Identity: `C:\Keys\operator.ppk`,
			},
			Mode: modeExec,
		},
	}

	wantMaster := []string{
		"-load", "ssh-handoff-A3B4-upstream", "-ssh", "-share", "-t",
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

func TestUniquePlinkTargetRejectsAnotherOwner(t *testing.T) {
	registry := &sessionRegistry{dir: filepath.Join(t.TempDir(), "runtime")}
	create := func(host string) *session {
		t.Helper()
		session, err := registry.create("", modeExec, connectionSpec{
			Host: host,
			User: "operator",
			Port: 2222,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { registry.remove(session.ID) })
		return session
	}
	first := create("Jump.Example.com")
	second := create("jump.example.com")

	err := ensureUniquePlinkTarget(registry, second)
	if err == nil || !strings.Contains(err.Error(), first.ID) {
		t.Fatalf("ensureUniquePlinkTarget() error = %v, want conflict with %s", err, first.ID)
	}
}
