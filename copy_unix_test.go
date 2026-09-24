//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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

	if err := os.WriteFile(localSource, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := copy("upload", session.ID, localSource, remoteTarget); code != 0 {
		t.Fatalf("empty upload exit code = %d", code)
	}
	assertFileContents(t, remoteTarget, nil)
	if code := copy("download", session.ID, remoteTarget, localTarget); code != 0 {
		t.Fatalf("empty download exit code = %d", code)
	}
	assertFileContents(t, localTarget, nil)
}

func TestRunTransferReportsLocalIOFailures(t *testing.T) {
	installFakeSSH(t)
	session := &session{
		Mode:       modeExec,
		Connection: connectionSpec{Host: "example.com", User: "operator", Port: 22},
	}
	for _, operation := range []string{"read", "write"} {
		t.Run(operation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
				t.Fatal(err)
			}
			flags := os.O_RDONLY
			if operation == "read" {
				flags = os.O_WRONLY
			}
			file, err := os.OpenFile(path, flags, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			var input io.Reader = strings.NewReader("data")
			var output io.Writer = io.Discard
			if operation == "read" {
				input = file
			} else {
				output = file
			}
			// Like SSH, the child reports command success despite its file I/O
			// failure. The original *os.File error must remain visible in Go.
			err = runTransfer(context.Background(), session, "cat; exit 0 # ssh-handoff-copy-test", input, output, io.Discard)
			var pathError *os.PathError
			if !errors.As(err, &pathError) || pathError.Op != operation || pathError.Path != path {
				t.Fatalf("runTransfer() = %v, want local %s error for %s", err, operation, path)
			}
		})
	}
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
