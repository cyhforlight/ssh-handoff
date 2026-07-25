//go:build windows

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestParsePlinkTarget(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    plinkTarget
	}{
		{
			name:    "destination and trailing port",
			command: "ssh JMS-token@jump.example.com -p 2222",
			want: plinkTarget{
				Host: "jump.example.com",
				User: "JMS-token",
				Port: 2222,
			},
		},
		{
			name:    "separate user and quoted identity",
			command: `ssh -6 -C -l operator -i "C:\Keys\work key.ppk" server.example.com`,
			want: plinkTarget{
				Host:         "server.example.com",
				User:         "operator",
				Port:         22,
				AddressFlag:  "-6",
				Compression:  true,
				IdentityFile: `C:\Keys\work key.ppk`,
			},
		},
		{
			name:    "attached options and IPv6",
			command: `ssh -p2200 -lroot root@[2001:db8::10]`,
			want: plinkTarget{
				Host: "2001:db8::10",
				User: "root",
				Port: 2200,
			},
		},
		{
			name:    "UNC identity path",
			command: `ssh -i "\\fileserver\keys\work key.ppk" operator@example.com`,
			want: plinkTarget{
				Host:         "example.com",
				User:         "operator",
				Port:         22,
				IdentityFile: `\\fileserver\keys\work key.ppk`,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parsePlinkTarget(test.command)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parsePlinkTarget() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParsePlinkTargetRejectsUnsupportedCommands(t *testing.T) {
	for _, command := range []string{
		"ssh host.example.com",
		"ssh -J bastion user@host.example.com",
		"ssh -o ProxyCommand=proxy user@host.example.com",
		"ssh -l first second@host.example.com",
		"ssh user@host.example.com uname -a",
		"ssh -4 -6 user@host.example.com",
		"ssh -p 0 user@host.example.com",
		"ssh -- user@host.example.com -p 2222",
		"ssh 'user@host.example.com",
		"plink user@host.example.com",
	} {
		if _, err := parsePlinkTarget(command); err == nil {
			t.Errorf("parsePlinkTarget(%q) unexpectedly succeeded", command)
		}
	}
}

func TestPlinkArgumentsPreserveExecutionMode(t *testing.T) {
	target := plinkTarget{
		Host:         "jump.example.com",
		User:         "JMS-token",
		Port:         2222,
		AddressFlag:  "-4",
		Compression:  true,
		IdentityFile: `C:\Keys\operator.ppk`,
	}
	session := &session{
		sessionInfo: sessionInfo{ID: "A3B4", Mode: modeExec},
	}

	master := plinkMasterArgs(session, target)
	for _, required := range []string{
		"-load", "ssh-handoff-A3B4-upstream", "-share", "-t",
		"-P", "2222", "-l", "JMS-token", "-4", "-C",
		`C:\Keys\operator.ppk`, "jump.example.com",
	} {
		if !slices.Contains(master, required) {
			t.Errorf("master arguments are missing %q: %#v", required, master)
		}
	}

	commandFile := `C:\Temp\ssh-handoff-command.txt`
	execArguments := plinkDownstreamArgs(session, target, commandFile)
	for _, required := range []string{
		"ssh-handoff-A3B4-downstream", "-share", "-batch",
		"-noagent", "-no-trivial-auth", "-T",
		"-no-sanitise-stdout", "-no-sanitise-stderr",
		"-m", commandFile,
	} {
		if !slices.Contains(execArguments, required) {
			t.Errorf("exec arguments are missing %q: %#v", required, execArguments)
		}
	}
	if slices.Contains(execArguments, "-t") {
		t.Fatalf("exec arguments unexpectedly request a PTY: %#v", execArguments)
	}
	if slices.Contains(execArguments, "-i") ||
		slices.Contains(execArguments, target.IdentityFile) {
		t.Fatalf("sharing downstream unexpectedly received authentication material: %#v", execArguments)
	}

	session.Mode = modeShellPTY
	ptyArguments := plinkDownstreamArgs(session, target, "")
	if !slices.Contains(ptyArguments, "-t") || slices.Contains(ptyArguments, "-T") {
		t.Fatalf("shell-pty arguments do not select a PTY: %#v", ptyArguments)
	}

	shareExistsArguments := plinkShareExistsArgs(session, target)
	if slices.Contains(shareExistsArguments, "-i") ||
		slices.Contains(shareExistsArguments, target.IdentityFile) {
		t.Fatalf("share check unexpectedly received authentication material: %#v", shareExistsArguments)
	}
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
	want, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolvePlinkPath() = %q, want %q", got, want)
	}
}

func TestUniquePlinkTargetRejectsAnotherOwner(t *testing.T) {
	registry := &sessionRegistry{dir: filepath.Join(t.TempDir(), "runtime")}
	first, err := registry.create("", modeExec, "ssh operator@Jump.Example.com -p 2222")
	if err != nil {
		t.Fatal(err)
	}
	defer registry.remove(first.ID)
	second, err := registry.create("", modeExec, "ssh operator@jump.example.com -p 2222")
	if err != nil {
		t.Fatal(err)
	}
	defer registry.remove(second.ID)

	target, err := parsePlinkTarget(second.Command)
	if err != nil {
		t.Fatal(err)
	}
	err = ensureUniquePlinkTarget(registry, second, target)
	if err == nil || !strings.Contains(err.Error(), first.ID) {
		t.Fatalf("ensureUniquePlinkTarget() error = %v, want conflict with %s", err, first.ID)
	}
}
