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
	created.State = stateManaged
	if err := registry.update(created); err != nil {
		t.Fatal(err)
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
		string(created.State),
		string(created.Mode),
		"operator@[2001:db8::10]:2222 identity=/keys/production",
		created.Note,
	} {
		if !strings.Contains(outputText, field) {
			t.Errorf("list output is missing %q: %s", field, outputText)
		}
	}
}

func TestRegistryRejectsLegacyCommandSession(t *testing.T) {
	registry := &sessionRegistry{dir: filepath.Join(t.TempDir(), "runtime")}
	if err := ensurePrivateDirectory(registry.dir); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"id":"ABCD","connection_command":"ssh operator@example.com","mode":"exec","state":"interactive","pid":1}`)
	if err := os.WriteFile(registry.statePath("ABCD"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.loadAll(); err == nil || !strings.Contains(err.Error(), "invalid connection") {
		t.Fatalf("registry.loadAll() error = %v, want invalid connection", err)
	}
}
