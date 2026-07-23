//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsurePrivateDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime")
	if err := ensurePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if permission := info.Mode().Perm(); permission != 0o700 {
		t.Fatalf("runtime directory permission = %o, want 700", permission)
	}
}
