package identity

import (
	"strings"
	"testing"
)

func TestStaticAddresses(t *testing.T) {
	id := Identity{Network: map[string]any{"networks": map[string]any{
		"20-b": map[string]any{"address": []any{"10.0.1.1/24"}},
		"10-a": map[string]any{"address": []any{"10.0.0.1/24", "fd00::1/64"}},
		"30-c": map[string]any{"DHCP": "yes"},
	}}}
	got := id.StaticAddresses()
	want := []string{"10.0.0.1", "fd00::1", "10.0.1.1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("StaticAddresses() = %v, want %v", got, want)
	}
}

func TestLayer(t *testing.T) {
	type part struct {
		Name string `json:"name"`
	}
	var id Identity
	var got part
	if ok, err := id.Layer("a", &got); ok || err != nil {
		t.Fatalf("Layer of an identity without layers = %v, %v; want false, nil", ok, err)
	}
	if err := id.SetLayer("a", part{Name: "n1"}); err != nil {
		t.Fatal(err)
	}
	if string(id.Layers["a"]) != `{"name":"n1"}` {
		t.Errorf("Layers[a] = %s", id.Layers["a"])
	}
	if ok, err := id.Layer("a", &got); !ok || err != nil || got.Name != "n1" {
		t.Errorf("Layer(a) = %v, %v, %+v; want true, nil, n1", ok, err, got)
	}
	if ok, err := id.Layer("b", &got); ok || err != nil {
		t.Errorf("Layer(b) = %v, %v; want false, nil", ok, err)
	}
	if err := id.SetLayer("n", (*part)(nil)); err == nil || id.Layers["n"] != nil {
		t.Errorf("SetLayer of a nil pointer = %v, part %s; want an error and no part", err, id.Layers["n"])
	}
	id.Layers["c"] = []byte(`[]`)
	if ok, err := id.Layer("c", &got); !ok || err == nil {
		t.Errorf("Layer(c) of a part that does not decode = %v, %v; want true and an error", ok, err)
	}
}
