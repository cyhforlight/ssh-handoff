//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionRegistryResolveIgnoresOtherRecords(t *testing.T) {
	registry := &sessionRegistry{dir: t.TempDir()}
	live, err := registry.create("", modeExec, connectionSpec{Host: "example.com", User: "operator", Port: 22})
	if err != nil {
		t.Fatal(err)
	}
	otherID := "ABCD"
	if otherID == live.ID {
		otherID = "EFGH"
	}
	path := registry.statePath(otherID)
	if err := os.Symlink(path, path); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.resolve(live.ID); err != nil {
		t.Fatalf("unrelated unreadable record affected resolve: %v", err)
	}
	if _, err := registry.list(); err == nil {
		t.Fatal("list hid a filesystem error")
	}
}

func TestSessionRegistryListIgnoresMissingRecords(t *testing.T) {
	registry := &sessionRegistry{dir: t.TempDir()}
	if err := ensurePrivateDirectory(registry.dir); err != nil {
		t.Fatal(err)
	}
	// ReadDir sees the entry, but ReadFile gets ENOENT, as when its owner exits.
	if err := os.Symlink(filepath.Join(registry.dir, "removed"), registry.statePath("ABCD")); err != nil {
		t.Fatal(err)
	}
	listed, err := registry.list()
	if err != nil || len(listed) != 0 {
		t.Fatalf("list with missing record = %#v, %v", listed, err)
	}
}
