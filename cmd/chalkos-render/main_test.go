package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const peers = `{"node": {"name": "cp1"}, "documents": {"Peer": [
  {"apiVersion": "bgp.example.org/v1alpha1", "kind": "Peer", "metadata": {"name": "a"}, "spec": {"address": "10.0.0.1", "asn": 64512, "tags": ["x", "y"]}},
  {"apiVersion": "bgp.example.org/v1alpha1", "kind": "Peer", "metadata": {"name": "b"}, "spec": {"address": "10.0.0.2", "asn": 4200000000, "description": "the \"other\" one"}}
]}}`

func render(t *testing.T, args []string, input string) (string, error) {
	t.Helper()
	out := t.TempDir()
	return out, run(args, strings.NewReader(input), out)
}

// A template reads the node and the documents, prints numbers exactly, and has join, quote, json
// and default; a key a document lacks reads as missing.
func TestTemplate(t *testing.T) {
	tmpl := filepath.Join(t.TempDir(), "bird.conf.tmpl")
	os.WriteFile(tmpl, []byte(`# {{ .node.name }}
{{ range .documents.Peer }}protocol bgp {{ .metadata.name }} { neighbor {{ .spec.address }} as {{ .spec.asn }}; description {{ quote (default "none" .spec.description) }}; tags {{ join "," (default (list) .spec.tags) }}; {{ json .spec.tags }} }
{{ end }}`), 0o644)
	// list is no function of the template.
	if _, err := render(t, []string{"template", tmpl, "bird.conf"}, peers); err == nil || !strings.Contains(err.Error(), `function "list" not defined`) {
		t.Fatalf("an undefined function: %v", err)
	}
	os.WriteFile(tmpl, []byte(`# {{ .node.name }}
{{ range .documents.Peer }}protocol bgp {{ .metadata.name }} { neighbor {{ .spec.address }} as {{ .spec.asn }}; description {{ quote (default "none" .spec.description) }}; {{ with .spec.tags }}tags {{ join "," . }}; {{ end }}{{ json .spec.tags }} }
{{ end }}`), 0o644)
	out, err := render(t, []string{"template", tmpl, "bird.conf"}, peers)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(out, "bird.conf"))
	want := `# cp1
protocol bgp a { neighbor 10.0.0.1 as 64512; description "none"; tags x,y; ["x","y"] }
protocol bgp b { neighbor 10.0.0.2 as 4200000000; description "the \"other\" one"; null }
`
	if string(got) != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", got, want)
	}
}

// documents writes each document of the kinds named as a file, and refuses a kind the input
// lacks.
func TestDocuments(t *testing.T) {
	out, err := render(t, []string{"documents", "Peer"}, peers)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(out, "Peer", "b.json"))
	if err != nil || !strings.Contains(string(b), `"asn": 4200000000`) {
		t.Errorf("Peer/b.json = %s, %v", b, err)
	}
	if _, err := render(t, []string{"documents", "Rack"}, peers); err == nil || !strings.Contains(err.Error(), "no kind Rack") {
		t.Errorf("an absent kind: %v", err)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"template", "x"}, {"documents"}, {"other", "x"}} {
		if _, err := render(t, args, peers); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if err := run([]string{"documents", "Peer"}, strings.NewReader(peers), ""); err == nil || !strings.Contains(err.Error(), "$OUT") {
		t.Errorf("without $OUT: %v", err)
	}
}
