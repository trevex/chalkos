package identity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/store"
)

const nodeIdentity = `{
  "hostname": "w1",
  "network": {},
  "networkUnits": {"10-uplink.network": "[Match]\nName=enp1s0\n"},
  "labels": {"zone": "a"},
  "taints": [],
  "storage": {}
}`

func newTestLoader(t *testing.T) (Loader, *string) {
	t.Helper()
	root := t.TempDir()
	hostname := ""
	l := Loader{
		Identity:    filepath.Join(root, "state", "identity.json"),
		RunDir:      filepath.Join(root, "run", "chalkos"),
		NetworkDir:  filepath.Join(root, "run", "systemd", "network"),
		Consumers:   filepath.Join(root, "etc", "consumers.json"),
		Config:      filepath.Join(root, "state", "config"),
		Schemas:     filepath.Join(root, "etc", "schemas"),
		SetHostname: func(name string) error { hostname = name; return nil },
	}
	write(t, l.Consumers, "{}")
	return l, &hostname
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestLoad(t *testing.T) {
	l, hostname := newTestLoader(t)
	write(t, l.Identity, nodeIdentity)
	// A unit from an earlier identity, and one an administrator placed there.
	write(t, filepath.Join(l.NetworkDir, "05-old.network"), Header+"[Match]\nName=eth9\n")
	write(t, filepath.Join(l.NetworkDir, "90-manual.network"), "[Match]\nName=eth8\n")

	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(l.RunDir, "node.json")); got != nodeIdentity {
		t.Errorf("node.json = %s", got)
	}
	if got := read(t, filepath.Join(l.NetworkDir, "10-uplink.network")); got != Header+"[Match]\nName=enp1s0\n" {
		t.Errorf("uplink unit = %q", got)
	}
	if _, err := os.Stat(filepath.Join(l.NetworkDir, "05-old.network")); !os.IsNotExist(err) {
		t.Error("kept a unit of an earlier identity")
	}
	if _, err := os.Stat(filepath.Join(l.NetworkDir, "90-manual.network")); err != nil {
		t.Error("removed a unit the loader did not write")
	}
	if *hostname != "w1" {
		t.Errorf("hostname = %q", *hostname)
	}
}

func TestLoadWithoutIdentity(t *testing.T) {
	l, hostname := newTestLoader(t)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.RunDir, "node.json")); !os.IsNotExist(err) || *hostname != "" {
		t.Error("applied an identity of a node that has none")
	}
}

func TestApplyRejectsUnitNames(t *testing.T) {
	l, _ := newTestLoader(t)
	for _, name := range []string{"../escape.network", "10-uplink.service"} {
		err := l.Apply([]byte(`{"networkUnits": {"` + name + `": ""}}`))
		if err == nil || !strings.Contains(err.Error(), "invalid networkd unit name") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestApplyWritesTimeServers(t *testing.T) {
	l, _ := newTestLoader(t)
	l.ChronySources = filepath.Join(l.RunDir, "chrony", "identity.sources")
	id := `{"hostname": "n1", "time": {"servers": [{"host": "ptbtime1.ptb.de", "nts": true}, {"host": "10.0.2.2", "nts": false, "port": 12300}]}}`
	if err := l.Apply([]byte(id)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(l.ChronySources)
	if err != nil {
		t.Fatal(err)
	}
	if want := "server ptbtime1.ptb.de iburst nts\nserver 10.0.2.2 iburst port 12300\n"; string(got) != want {
		t.Errorf("sources = %q, want %q", got, want)
	}
	// An identity without servers leaves chrony none.
	if err := l.Apply([]byte(`{"hostname": "n1"}`)); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(l.ChronySources); len(got) != 0 {
		t.Errorf("sources = %q, want none", got)
	}
	for _, host := range []string{"a b", "x\nserver evil.example iburst", "-x", ""} {
		bad, _ := json.Marshal(map[string]any{"time": map[string]any{"servers": []any{map[string]any{"host": host}}}})
		if err := l.Apply(bad); err == nil {
			t.Errorf("host %q: applied", host)
		}
	}
}

func TestTimeChanged(t *testing.T) {
	a := []byte(`{"time": {"servers": [{"host": "a", "nts": true}]}}`)
	b := []byte(`{"hostname": "other", "time": {"servers": [{"host": "a", "nts": true}]}}`)
	c := []byte(`{"time": {"servers": [{"host": "a", "nts": false}]}}`)
	if TimeChanged(a, b) {
		t.Error("the same servers changed")
	}
	if !TimeChanged(a, c) || !TimeChanged(nil, a) {
		t.Error("other servers did not change")
	}
}

const loaderConfig = `
apiVersion: demo.example.org/v1alpha1
kind: Demo
metadata: {name: one}
spec: {name: first, token: {secretRef: {name: demo, key: token}}}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: demo}
spec: {data: {token: s3cret}}
`

// copySchemas copies the fixture schemas of the built-in kinds and the Demo kind into dir.
func copySchemas(t *testing.T, dir string) {
	t.Helper()
	for _, src := range []string{"../../test/fixtures/kinds/builtin", "../../test/fixtures/kinds/image/schemas"} {
		entries, err := os.ReadDir(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			write(t, filepath.Join(dir, e.Name()), read(t, filepath.Join(src, e.Name())))
		}
	}
}

func TestLoadWritesConfig(t *testing.T) {
	l, _ := newTestLoader(t)
	copySchemas(t, l.Schemas)
	write(t, l.Identity, nodeIdentity)
	write(t, l.Consumers, `{
	  "demo": {"kinds": ["Demo"], "restartOnChange": true},
	  "idle": {"kinds": ["Cluster"], "restartOnChange": true}
	}`)
	docs, err := config.Parse(strings.NewReader(loaderConfig))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Write(l.Config, store.Stored{Documents: docs, Version: "v1"}); err != nil {
		t.Fatal(err)
	}

	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(l.RunDir, "node.json")); got != nodeIdentity {
		t.Errorf("node.json = %s", got)
	}
	if got := read(t, filepath.Join(l.RunDir, "config", "demo.example.org", "Demo", "one.json")); !strings.Contains(got, `"name":"first"`) {
		t.Errorf("Demo = %s", got)
	}
	if _, err := os.Stat(filepath.Join(l.RunDir, "config", "chalkos.dev", "Secret")); !os.IsNotExist(err) {
		t.Error("wrote the Secret below /run/chalkos/config")
	}
	if got := read(t, filepath.Join(l.RunDir, "credentials", "demo", "demo.token")); got != "s3cret" {
		t.Errorf("credential = %q", got)
	}
	if entries, err := os.ReadDir(filepath.Join(l.RunDir, "credentials", "idle")); err != nil || len(entries) != 0 {
		t.Errorf("idle's credentials = %v, %v; want an empty directory", entries, err)
	}
}

func TestLoadWithoutConfigMakesCredentialDirectories(t *testing.T) {
	l, _ := newTestLoader(t)
	write(t, l.Identity, nodeIdentity)
	write(t, l.Consumers, `{"demo": {"kinds": ["Demo"], "restartOnChange": true}}`)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(filepath.Join(l.RunDir, "credentials", "demo")); err != nil || len(entries) != 0 {
		t.Errorf("demo's credentials = %v, %v; want an empty directory", entries, err)
	}
}

func TestKindRestarts(t *testing.T) {
	consumers := map[string]Consumer{
		"demo":    {Kinds: []string{"Demo"}, RestartOnChange: true},
		"network": {Kinds: []string{"Network", "Link"}, RestartOnChange: true},
		"quiet":   {Kinds: []string{"Demo"}, RestartOnChange: false},
	}
	before := map[string]string{"Demo": "1", "Network": "2"}
	for name, c := range map[string]struct {
		after map[string]string
		want  []string
	}{
		"unchanged":    {map[string]string{"Demo": "1", "Network": "2"}, nil},
		"kind changed": {map[string]string{"Demo": "3", "Network": "2"}, []string{"demo"}},
		"kind removed": {map[string]string{"Demo": "1"}, []string{"network"}},
		"kind added":   {map[string]string{"Demo": "1", "Network": "2", "Link": "4"}, []string{"network"}},
		"all removed":  {map[string]string{}, []string{"demo", "network"}},
	} {
		if got := KindRestarts(consumers, before, c.after); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: restarts = %v, want %v", name, got, c.want)
		}
	}
}

func TestApplyWritesNoKeyCredentials(t *testing.T) {
	l, _ := newTestLoader(t)
	write(t, l.Consumers, `{"report": {"keys": ["hostname"], "restartOnChange": true}}`)
	if err := l.Apply([]byte(nodeIdentity)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.RunDir, "credentials", "hostname")); !os.IsNotExist(err) {
		t.Errorf("wrote the credential of an identity key: %v", err)
	}
}
