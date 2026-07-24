package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestRegistryPersistsSessionAndPublishesInfo(t *testing.T) {
	registry := &sessionRegistry{dir: t.TempDir()}
	created, err := registry.create("production", modeShellPTY, "ssh jump-alias")
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
	if !reflect.DeepEqual(loaded, created) {
		t.Fatalf("persisted session is incomplete: %#v", loaded)
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
		created.Command,
		created.Note,
	} {
		if !strings.Contains(outputText, field) {
			t.Errorf("list output is missing %q: %s", field, outputText)
		}
	}
}
