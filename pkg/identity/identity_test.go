package identity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const nodeIdentity = `{
  "hostname": "w1",
  "network": {},
  "networkUnits": {"10-uplink.network": "[Match]\nName=enp1s0\n"},
  "labels": {"zone": "a"},
  "taints": [],
  "storage": {},
  "extensions": {"rack": {"location": "rack-a/u14", "slots": [1, 2]}}
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
		SetHostname: func(name string) error { hostname = name; return nil },
	}
	write(t, l.Consumers, `{
	  "rack-location": {"keys": ["rack.location"], "restartOnChange": true},
	  "rack-slots": {"keys": ["rack.slots", "missing.key"], "restartOnChange": true},
	  "labeller": {"keys": ["labels"], "restartOnChange": false}
	}`)
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
	creds := filepath.Join(l.RunDir, "credentials")
	for key, want := range map[string]string{
		"rack.location": "rack-a/u14",
		"rack.slots":    "[1,2]",
		"labels":        `{"zone":"a"}`,
	} {
		if got := read(t, filepath.Join(creds, key)); got != want {
			t.Errorf("credential %s = %q, want %q", key, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(creds, "missing.key")); !os.IsNotExist(err) {
		t.Errorf("wrote a credential for a key the identity lacks: %v", err)
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

func TestApplyRemovesCredentialOfRemovedKey(t *testing.T) {
	l, _ := newTestLoader(t)
	if err := l.Apply([]byte(nodeIdentity)); err != nil {
		t.Fatal(err)
	}
	if err := l.Apply([]byte(`{"hostname": "w1", "extensions": {}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.RunDir, "credentials", "rack.location")); !os.IsNotExist(err) {
		t.Error("kept the credential of a key the identity no longer has")
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

func TestRestarts(t *testing.T) {
	l, _ := newTestLoader(t)
	consumers, err := ReadConsumers(l.Consumers)
	if err != nil {
		t.Fatal(err)
	}
	moved := strings.Replace(nodeIdentity, "rack-a/u14", "rack-b/u2", 1)
	relabelled := strings.Replace(nodeIdentity, `"zone": "a"`, `"zone": "b"`, 1)
	for name, c := range map[string]struct {
		new  string
		want []string
	}{
		"unchanged":            {nodeIdentity, nil},
		"key changed":          {moved, []string{"rack-location"}},
		"no restart requested": {relabelled, nil},
		"keys removed":         {`{"extensions": {}}`, []string{"rack-location", "rack-slots"}},
	} {
		got, err := Restarts(consumers, []byte(nodeIdentity), []byte(c.new))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: restarts = %v, want %v", name, got, c.want)
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
