package config

import (
	"strings"
	"testing"
)

// TestEncode writes documents as the YAML stream chalkctl sends, one JSON document each, with the
// version chalkd computes from that text.
func TestEncode(t *testing.T) {
	docs, err := Parse(strings.NewReader("apiVersion: chalkos.dev/v1alpha1\nkind: Host\nmetadata: {name: default}\nspec: {}\n---\napiVersion: time.chalkos.dev/v1alpha1\nkind: TimeSync\nmetadata: {name: time}\nspec: {servers: [{host: ntp.lan, port: 123}]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := Encode(docs)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"apiVersion":"chalkos.dev/v1alpha1","kind":"Host","metadata":{"name":"default"},"spec":{}}` + "\n---\n" +
		`{"apiVersion":"time.chalkos.dev/v1alpha1","kind":"TimeSync","metadata":{"name":"time"},"spec":{"servers":[{"host":"ntp.lan","port":123}]}}` + "\n"
	if e.Text != want {
		t.Errorf("text =\n%s\nwant\n%s", e.Text, want)
	}
	read, err := Parse(strings.NewReader(e.Text))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := Hash(read); e.Version != v || len(e.Documents) != 2 {
		t.Errorf("version %s of %d documents, want %s of 2", e.Version, len(e.Documents), v)
	}
}

// TestEncodeVersionIgnoresOrder gives documents the same version in every order, so the order of
// the files that hold them does not change a node's config version.
func TestEncodeVersionIgnoresOrder(t *testing.T) {
	docs, err := Parse(strings.NewReader("apiVersion: chalkos.dev/v1alpha1\nkind: Host\nmetadata: {name: default}\nspec: {}\n---\napiVersion: chalkos.dev/v1alpha1\nkind: Secret\nmetadata: {name: b}\nspec: {data: {k: b}}\n---\napiVersion: chalkos.dev/v1alpha1\nkind: Secret\nmetadata: {name: a}\nspec: {data: {k: a}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := Encode(docs)
	if err != nil {
		t.Fatal(err)
	}
	for _, order := range [][]int{{0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		var permuted []Document
		for _, i := range order {
			permuted = append(permuted, docs[i])
		}
		e, err := Encode(permuted)
		if err != nil {
			t.Fatal(err)
		}
		if e.Version != first.Version {
			t.Errorf("version in order %v = %s, want %s", order, e.Version, first.Version)
		}
	}
}
