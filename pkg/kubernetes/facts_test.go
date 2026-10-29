package kubernetes

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestFactsPayload(t *testing.T) {
	p, err := FactsPayload([]byte(imageJSON))
	if err != nil {
		t.Fatal(err)
	}
	img, ok, err := ImageOfPayload(p)
	if err != nil || !ok {
		t.Fatalf("ImageOfPayload = %v, %v", ok, err)
	}
	if img.Version != "1.37.1" || img.CNI != CNIFlannel {
		t.Errorf("ImageOfPayload = %+v", img)
	}
	other, err := anypb.New(wrapperspb.String("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ImageOfPayload(other); ok || err != nil {
		t.Errorf("ImageOfPayload of another type = %v, %v; want false, nil", ok, err)
	}
}

func TestFactsPayloadOf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.json")
	if p, err := FactsPayloadOf(path); p != nil || err != nil {
		t.Errorf("FactsPayloadOf a missing file = %v, %v; want nil, nil", p, err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if p, err := FactsPayloadOf(path); p != nil || err != nil {
		t.Errorf("FactsPayloadOf an empty file = %v, %v; want nil, nil", p, err)
	}
	if err := os.WriteFile(path, []byte(imageJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := FactsPayloadOf(path)
	if err != nil {
		t.Fatal(err)
	}
	if img, ok, err := ImageOfPayload(p); !ok || err != nil || img.Version != "1.37.1" {
		t.Errorf("ImageOfPayload = %+v, %v, %v", img, ok, err)
	}
}
