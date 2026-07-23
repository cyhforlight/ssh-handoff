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

	var output bytes.Buffer
	if code := listCommand(registry, nil, &output); code != 0 {
		t.Fatalf("listCommand() code = %d, output = %s", code, output.String())
	}
	json := output.String()
	for _, privateField := range []string{"control_path", "pid"} {
		if strings.Contains(json, `"`+privateField+`"`) {
			t.Errorf("list output exposes private field %q: %s", privateField, json)
		}
	}
	if !strings.Contains(json, `"connection_command":"ssh jump-alias"`) || !strings.Contains(json, `"note":"production"`) || !strings.Contains(json, `"state":"managed"`) {
		t.Fatalf("list output is missing public state: %s", json)
	}
}
