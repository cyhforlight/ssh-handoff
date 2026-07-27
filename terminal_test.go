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
	initialWant := "whoami\n" + noopCommand + noopCommand + "pwd\n"
	if got := remote.String(); got != initialWant {
		t.Fatalf("remote input = %q, want %q", got, initialWant)
	}

	if err := controller.keepalive(); err != nil {
		t.Fatal(err)
	}
	if remote.String() != initialWant {
		t.Fatal("interactive state must not write keepalive input")
	}

	if _, err := controller.handleInput([]byte{handoffByte}); err != nil {
		t.Fatal(err)
	}
	if controller.keepaliveTick == nil {
		t.Fatal("managed state must schedule keepalive")
	}
	if err := controller.keepalive(); err != nil {
		t.Fatal(err)
	}
	if want := initialWant + noopCommand + noopCommand; remote.String() != want {
		t.Fatalf("managed input = %q, want %q", remote.String(), want)
	}

	if _, err := controller.handleInput([]byte{handoffByte}); err != nil {
		t.Fatal(err)
	}
	if controller.keepaliveTick != nil {
		t.Fatal("returning to interactive state must stop keepalive")
	}
}
