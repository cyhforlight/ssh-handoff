package main

import (
	"bytes"
	"testing"
)

func TestHandoffControllerSerializesInputAndKeepalive(t *testing.T) {
	var remote bytes.Buffer
	controller := handoffController{remote: &remote}

	changed, err := controller.handleInput([]byte("whoami\n\x1dignored\x1dpwd\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !changed || controller.managed {
		t.Fatalf("two handoffs should return to interactive state: changed=%v managed=%v", changed, controller.managed)
	}
	if want := "whoami\n:\n:\npwd\n"; remote.String() != want {
		t.Fatalf("remote input = %q, want %q", remote.String(), want)
	}

	if err := controller.keepalive(); err != nil {
		t.Fatal(err)
	}
	if remote.String() != "whoami\n:\n:\npwd\n" {
		t.Fatal("interactive state must not write keepalive input")
	}

	if _, err := controller.handleInput([]byte{handoffByte}); err != nil {
		t.Fatal(err)
	}
	if err := controller.keepalive(); err != nil {
		t.Fatal(err)
	}
	if want := "whoami\n:\n:\npwd\n:\n:\n"; remote.String() != want {
		t.Fatalf("managed input = %q, want %q", remote.String(), want)
	}
}
