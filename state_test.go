package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestSessionRegistryLifecycle(t *testing.T) {
	registry := &sessionRegistry{dir: t.TempDir()}
	connection := connectionSpec{Host: "example.com", User: "operator", Port: 22}
	live, err := registry.create("active", modeExec, connection)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := registry.create("stale", modeExec, connection)
	if err != nil {
		t.Fatal(err)
	}
	stale.PID = 0
	if err := registry.update(stale); err != nil {
		t.Fatal(err)
	}

	resolved, err := registry.resolve(strings.ToLower(live.ID))
	if err != nil || resolved.ID != live.ID {
		t.Fatalf("resolve live session = %#v, %v", resolved, err)
	}
	if _, err := registry.resolve(stale.ID); !errors.Is(err, errSessionNotFound) {
		t.Fatalf("resolve stale session = %v, want not found", err)
	}
	listed, err := registry.list()
	if err != nil || len(listed) != 1 || listed[0].ID != live.ID {
		t.Fatalf("list = %#v, %v; want only %s", listed, err, live.ID)
	}
	if _, err := os.Stat(registry.statePath(stale.ID)); err != nil {
		t.Fatalf("queries removed stale session: %v", err)
	}

	created, err := registry.create("new", modeShellPTY, connection)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == live.ID {
		t.Fatal("create reused a live session ID")
	}
	if created.ID != stale.ID {
		if _, err := os.Stat(registry.statePath(stale.ID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("create did not reclaim stale record: %v", err)
		}
	}
	registry.removeFiles(live.ID)
	if _, err := registry.resolve(live.ID); !errors.Is(err, errSessionNotFound) {
		t.Fatalf("resolve removed session = %v, want not found", err)
	}
}

func TestSessionRegistryResolveRejectsPaths(t *testing.T) {
	registry := &sessionRegistry{dir: t.TempDir()}
	for _, id := range []string{"", "../ABCD", `..\ABCD`, "ABCD.json"} {
		if _, err := registry.resolve(id); !errors.Is(err, errSessionNotFound) {
			t.Errorf("resolve(%q) = %v, want not found", id, err)
		}
	}
}
