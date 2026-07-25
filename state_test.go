package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryTreatsMissingDirectoryAsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime")
	registry := &sessionRegistry{dir: path}

	sessions, err := registry.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("registry.list() returned %d sessions, want none", len(sessions))
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("registry.list() materialized runtime directory: %v", err)
	}
	if _, err := registry.resolve("ABCD"); !errors.Is(err, errSessionNotFound) {
		t.Fatalf("registry.resolve() error = %v, want errSessionNotFound", err)
	}
}

func TestRegistryPersistsSessionAndPublishesInfo(t *testing.T) {
	registry := &sessionRegistry{dir: filepath.Join(t.TempDir(), "runtime")}
	connection := connectionSpec{
		Host:     "2001:db8::10",
		User:     "operator",
		Port:     2222,
		Identity: "/keys/production",
	}
	created, err := registry.create("production", modeShellPTY, connection)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.ID) != 4 {
		t.Fatalf("session ID length = %d, want 4: %s", len(created.ID), created.ID)
	}

	loaded, err := registry.resolve(strings.ToLower(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	if *loaded != *created {
		t.Fatalf("persisted session is incomplete: %#v", loaded)
	}
	data, err := os.ReadFile(registry.statePath(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "connection_command") ||
		!strings.Contains(string(data), `"connection"`) {
		t.Fatalf("session JSON uses the wrong connection shape: %s", data)
	}

	var stdout, stderr bytes.Buffer
	if code := listCommand(registry, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("listCommand() code = %d, stderr = %s", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("listCommand() stderr = %s", stderr.String())
	}
	outputText := stdout.String()
	for _, field := range []string{
		created.ID,
		string(created.Mode),
		"operator@[2001:db8::10]:2222 identity=/keys/production",
		created.Note,
	} {
		if !strings.Contains(outputText, field) {
			t.Errorf("list output is missing %q: %s", field, outputText)
		}
	}
}

func TestRegistrySkipsUnusableSessionFiles(t *testing.T) {
	registry := &sessionRegistry{dir: filepath.Join(t.TempDir(), "runtime")}
	if err := ensurePrivateDirectory(registry.dir); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"broken.json": `{`,
		"legacy.json": `{"id":"ABCD","connection_command":"ssh operator@example.com","mode":"exec","state":"interactive","pid":1}`,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(registry.dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	created, err := registry.create("", modeExec, connectionSpec{
		Host: "example.com",
		User: "operator",
		Port: 22,
	})
	if err != nil {
		t.Fatalf("registry.create() with unusable session files: %v", err)
	}
	sessions, err := registry.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != created.ID {
		t.Fatalf("registry.list() = %#v, want only session %s", sessions, created.ID)
	}
}

func TestRegistryCleanupPreservesSessionLock(t *testing.T) {
	registry := &sessionRegistry{dir: filepath.Join(t.TempDir(), "runtime")}
	if err := ensurePrivateDirectory(registry.dir); err != nil {
		t.Fatal(err)
	}
	session := &session{
		ID:         "ABCD",
		Connection: connectionSpec{Host: "example.com", User: "operator", Port: 22},
		Mode:       modeExec,
		PID:        -1,
	}
	if err := registry.write(session); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(registry.dir, session.ID+".lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	sessions, err := registry.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("registry.list() returned dead session: %#v", sessions)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("registry cleanup removed stable session lock: %v", err)
	}
}
