package chalkcfg

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeFile writes data to path, creating its directories.
func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// testNode is a node's config directory and its image's schemas, laid out as chalkd and the image
// lay them out. The node has two Peers, backup with neighbors, and a Rack. The image also declares
// Site, of which the node has no document, and Secret.
func testNode(t *testing.T) store {
	t.Helper()
	dir := t.TempDir()
	s := store{config: filepath.Join(dir, "config"), schemas: filepath.Join(dir, "schemas")}
	for _, name := range []string{
		"bgp.example.org_v1alpha1_Peer.json",
		"rack.example.org_v1alpha1_Rack.json",
		"site.example.org_v1alpha1_Site.json",
		"chalkos.dev_v1alpha1_Secret.json",
	} {
		writeFile(t, filepath.Join(s.schemas, name), "{}\n")
	}
	writeFile(t, filepath.Join(s.config, "rack.example.org", "Rack", "rack.json"),
		`{"apiVersion":"rack.example.org/v1alpha1","kind":"Rack","metadata":{"name":"rack"},"spec":{"location":"rack-a/u12"}}`+"\n")
	writeFile(t, filepath.Join(s.config, "bgp.example.org", "Peer", "uplink.json"),
		`{"apiVersion":"bgp.example.org/v1alpha1","kind":"Peer","metadata":{"name":"uplink"},"spec":{"asn":4200000001,"port":179,"tags":["a","b"],"labels":{"zone-a":"z1"}}}`+"\n")
	writeFile(t, filepath.Join(s.config, "bgp.example.org", "Peer", "backup.json"),
		`{"apiVersion":"bgp.example.org/v1alpha1","kind":"Peer","metadata":{"name":"backup"},"spec":{"asn":4200000002,"port":1790,"tags":[],"labels":{},"neighbors":[{"ip":"10.0.0.1","asn":65001},{"ip":"10.0.0.2","asn":65002}]}}`+"\n")
	return s
}

func names(docs []document) []string {
	var out []string
	for _, d := range docs {
		out = append(out, d.name)
	}
	return out
}

func TestKinds(t *testing.T) {
	kinds, err := testNode(t).kinds()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]kindInfo{
		"Peer":   {groups: []string{"bgp.example.org"}, documents: 2},
		"Rack":   {groups: []string{"rack.example.org"}, documents: 1},
		"Site":   {groups: []string{"site.example.org"}, documents: 0},
		"Secret": {groups: []string{"chalkos.dev"}, documents: 0},
	}
	got := map[string]kindInfo{}
	for k, v := range kinds {
		got[k] = *v
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("kinds = %+v, want %+v", got, want)
	}
}

func TestDocuments(t *testing.T) {
	s := testNode(t)
	docs, err := s.documents("Peer", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(docs); !reflect.DeepEqual(got, []string{"backup", "uplink"}) {
		t.Errorf("Peers = %v, want backup and uplink in name order", got)
	}
	spec := docs[1].object["spec"].(map[string]any)
	if asn := spec["asn"]; asn != json.Number("4200000001") {
		t.Errorf("asn = %#v, want the number with its digits", asn)
	}

	docs, err = s.documents("Rack", "rack")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].kind != "Rack" || docs[0].object["spec"].(map[string]any)["location"] != "rack-a/u12" {
		t.Errorf("Rack rack = %+v", docs)
	}

	docs, err = s.documents("Site", "")
	if err != nil || len(docs) != 0 {
		t.Errorf("Site = %v, %v; want no documents and no error", docs, err)
	}
}

func TestDocumentsRefused(t *testing.T) {
	s := testNode(t)
	for _, c := range []struct{ kind, name, want string }{
		{"Nope", "", "the image declares no kind Nope; it declares Peer, Rack, Site"},
		{"Rack", "other", `the node has no Rack named "other"`},
		{"Rack", "../Rack/rack", `the node has no Rack named "../Rack/rack"`},
		{"Site", "main", `the node has no Site named "main"`},
		{"Secret", "", "Secrets are never printed"},
	} {
		_, err := s.documents(c.kind, c.name)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %q: err = %v, want %q", c.kind, c.name, err, c.want)
		}
	}
	if _, err := s.documents("Secret", ""); !errors.Is(err, errSecret) {
		t.Errorf("Secret: err = %v, want errSecret", err)
	}
}

func TestKindInTwoGroups(t *testing.T) {
	s := testNode(t)
	writeFile(t, filepath.Join(s.config, "other.example.org", "Rack", "x.json"), `{"kind":"Rack"}`+"\n")
	_, err := s.documents("Rack", "")
	if err == nil || !strings.Contains(err.Error(), "the kind Rack is in more than one group (other.example.org, rack.example.org)") {
		t.Errorf("err = %v", err)
	}
}

// Without schemas, chalkcfg knows the kinds that have documents.
func TestWithoutSchemas(t *testing.T) {
	s := testNode(t)
	s.schemas = filepath.Join(t.TempDir(), "none")
	if docs, err := s.documents("Rack", "rack"); err != nil || len(docs) != 1 {
		t.Errorf("Rack rack = %v, %v", docs, err)
	}
	if _, err := s.documents("Site", ""); err == nil || !strings.Contains(err.Error(), "declares no kind Site") {
		t.Errorf("Site: err = %v", err)
	}
}

// A node without config has no /run/chalkos/config.
func TestWithoutConfig(t *testing.T) {
	s := testNode(t)
	s.config = filepath.Join(t.TempDir(), "none")
	if docs, err := s.documents("Rack", ""); err != nil || len(docs) != 0 {
		t.Errorf("Rack = %v, %v; want no documents", docs, err)
	}
}

// A store resolved once keeps reading the config it resolved while chalkd replaces the config.
func TestResolve(t *testing.T) {
	s := testNode(t)
	first, second := s.config+"-a", s.config+"-b"
	if err := os.Rename(s.config, first); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(first), s.config); err != nil {
		t.Fatal(err)
	}
	r, err := s.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(second, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.config); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(second), s.config); err != nil {
		t.Fatal(err)
	}
	if docs, err := r.documents("Rack", "rack"); err != nil || len(docs) != 1 {
		t.Errorf("Rack rack = %v, %v; want the document of the config resolved", docs, err)
	}

	s.config = filepath.Join(t.TempDir(), "none")
	if r, err := s.resolve(); err != nil || r.config != s.config {
		t.Errorf("resolve without config = %+v, %v", r, err)
	}
}
