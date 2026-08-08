//go:build linux || darwin

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyCommand(t *testing.T) {
	installFakeSSH(t)
	dir := t.TempDir()
	registry := &sessionRegistry{dir: dir}
	session, err := registry.create(
		"",
		modeExec,
		connectionSpec{Host: "example.com", User: "operator", Port: 22},
	)
	if err != nil {
		t.Fatal(err)
	}
	copy := func(arguments ...string) int {
		return copyCommand(registry, arguments, io.Discard, io.Discard)
	}

	data := []byte{'b', 'i', 'n', 0, '\n', 0xff}
	localSource := filepath.Join(dir, "local source.bin")
	remoteTarget := filepath.Join(dir, "ssh-handoff-copy-test's remote.bin")
	localTarget := filepath.Join(dir, "local target.bin")
	if err := os.WriteFile(localSource, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := copy("upload", session.ID, localSource, remoteTarget); code != 0 {
		t.Fatalf("upload exit code = %d", code)
	}
	assertFileContents(t, remoteTarget, data)
	if code := copy("upload", session.ID, dir, remoteTarget); code == 0 {
		t.Fatal("uploading a directory succeeded")
	}
	assertFileContents(t, remoteTarget, data)
	if code := copy("download", session.ID, remoteTarget, localTarget); code != 0 {
		t.Fatalf("download exit code = %d", code)
	}
	assertFileContents(t, localTarget, data)
	if code := copy("download", session.ID, "ssh-handoff-copy-failure", localTarget); code == 0 {
		t.Fatal("failed download succeeded")
	}
	assertFileContents(t, localTarget, data)
}

func assertFileContents(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s contents differ: got %d bytes, want %d", path, len(got), len(want))
	}
}
