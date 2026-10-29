package pki

import (
	"testing"

	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestPayload(t *testing.T) {
	p, err := Payload([]byte(`{"kind":"worker"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !IsPayload(p) {
		t.Fatal("IsPayload of a share = false")
	}
	share, err := SharePayload(p)
	if err != nil || string(share) != `{"kind":"worker"}` {
		t.Errorf("SharePayload = %q, %v", share, err)
	}
	other, err := anypb.New(wrapperspb.String("x"))
	if err != nil {
		t.Fatal(err)
	}
	if IsPayload(other) {
		t.Error("IsPayload of another type = true")
	}
}
