package config

import (
	"strings"
	"testing"
)

// TestEncode writes documents as the YAML stream chalkctl sends, one JSON document each, with the
// version chalkd computes from that text.
func TestEncode(t *testing.T) {
	docs, err := Parse(strings.NewReader("apiVersion: chalkos.dev/v1alpha1\nkind: Cluster\nmetadata: {name: lab}\nspec: {}\n---\napiVersion: time.chalkos.dev/v1alpha1\nkind: TimeSync\nmetadata: {name: time}\nspec: {servers: [{host: ntp.lan, port: 123}]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := Encode(docs)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"apiVersion":"chalkos.dev/v1alpha1","kind":"Cluster","metadata":{"name":"lab"},"spec":{}}` + "\n---\n" +
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
