package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestRegistryPersistsSessionAndPublishesOnlySummary(t *testing.T) {
	registry := &sessionRegistry{dir: t.TempDir()}
	created, err := registry.create("prod", modeShellPTY, "ssh jump-alias")
	if err != nil {
		t.Fatal(err)
	}
	created.State = stateManaged
	if err := registry.update(created); err != nil {
		t.Fatal(err)
	}

	loaded, err := registry.resolve("prod")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != created.ID || loaded.Command != "ssh jump-alias" || loaded.Platform != created.Platform || loaded.PID != os.Getpid() || loaded.State != stateManaged {
		t.Fatalf("persisted session is incomplete: %#v", loaded)
	}

	if _, err := registry.create("prod", modeExec, "ssh other"); !errors.Is(err, errNameInUse) {
		t.Fatalf("duplicate name error = %v, want %v", err, errNameInUse)
	}

	var output bytes.Buffer
	if code := listCommand(registry, nil, &output); code != 0 {
		t.Fatalf("listCommand() code = %d, output = %s", code, output.String())
	}
	for _, privateField := range []string{"command", "control_path", "pid"} {
		if strings.Contains(output.String(), `"`+privateField+`"`) {
			t.Errorf("list output exposes private field %q: %s", privateField, output.String())
		}
	}
	if !strings.Contains(output.String(), `"name":"prod"`) || !strings.Contains(output.String(), `"state":"managed"`) {
		t.Fatalf("list output is missing public state: %s", output.String())
	}
}
