package effects

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "effects.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Read reads each effect's triggers and actions.
func TestRead(t *testing.T) {
	path := write(t, `{"networkd": {"when": "facts.units != oldFacts.units", "reload": ["systemd-networkd.service"]},
	  "kubelet": {"outputs": ["cluster"], "restart": ["kubelet.service"], "start": true, "chalkd": "kubernetes:restart"},
	  "rack": {"kinds": ["Rack"], "restart": ["rack.service"]}}`)
	got, err := Read(path)
	want := map[string]Definition{
		"networkd": {When: "facts.units != oldFacts.units", Reload: []string{"systemd-networkd.service"}},
		"kubelet":  {Outputs: []string{"cluster"}, Restart: []string{"kubelet.service"}, Start: true, Chalkd: "kubernetes:restart"},
		"rack":     {Kinds: []string{"Rack"}, Restart: []string{"rack.service"}},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("effects = %+v, %v; want %+v", got, err, want)
	}
	if got, err := Read(filepath.Join(t.TempDir(), "missing.json")); err != nil || len(got) != 0 {
		t.Errorf("a missing file: %v, %v", got, err)
	}
}

// Read refuses an effect chalkd could not carry out: one nothing triggers, one that does
// nothing, a unit that is no unit name, a chalkd action that names no layer's action, a hint
// without text or with a relative file, and a command that names no program it can run, since
// chalkd splits the command on spaces and runs the first word, from its PATH or the image's store.
func TestReadRefuses(t *testing.T) {
	for name, tc := range map[string]struct{ effect, want string }{
		"no trigger":             {`{"reload": ["a.service"]}`, "effect e: it has none of outputs, kinds and when"},
		"no action":              {`{"when": "true"}`, "effect e: it restarts, reloads and runs nothing"},
		"a unit without suffix":  {`{"when": "true", "restart": ["kubelet"]}`, `"kubelet" is no unit name`},
		"a chalkd action":        {`{"when": "true", "chalkd": "stop"}`, `chalkd is "stop", not <layer>:<action>`},
		"a hint without text":    {`{"when": "true", "restart": ["a.service"], "hint": {"text": ""}}`, "effect e: its hint has no text"},
		"a hint's relative file": {`{"when": "true", "restart": ["a.service"], "hint": {"text": "x", "file": "rel"}}`, `its hint's file "rel" is no absolute path`},
		"spaces":                 {`{"when": "true", "run": "  "}`, "effect e: the command to run is empty"},
		"outside the store":      {`{"when": "true", "run": "/usr/bin/chronyc reload sources"}`, `the command "/usr/bin/chronyc reload sources" names /usr/bin/chronyc, which is neither`},
		"a relative path":        {`{"when": "true", "run": "bin/chronyc reload"}`, "names bin/chronyc, which is neither"},
		"a bare program":         {`{"when": "true", "run": "chronyc reload sources"}`, ""},
		"a program of the store": {`{"outputs": ["x"], "run": "/nix/store/x-chrony/bin/chronyc reload sources"}`, ""},
		"chalkd alone":           {`{"when": "true", "chalkd": "kubernetes:reload"}`, ""},
		"kinds alone":            {`{"kinds": ["Rack"], "restart": ["a.service"]}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Read(write(t, `{"e": `+tc.effect+`}`))
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// Read refuses two effects that give one unit different hints, since chalkd looks a hint up by
// its unit; the same hint twice, or hints of different units, pass.
func TestReadRefusesConflictingHints(t *testing.T) {
	for name, tc := range map[string]struct{ b, want string }{
		"another text":  {`{"when": "true", "restart": ["a.service"], "hint": {"text": "y"}}`, "effects a and b give a.service different hints"},
		"another file":  {`{"when": "true", "restart": ["a.service"], "hint": {"text": "x", "file": "/run/y"}}`, "effects a and b give a.service different hints"},
		"the same hint": {`{"when": "true", "restart": ["a.service"], "hint": {"text": "x", "file": "/run/x"}}`, ""},
		"another unit":  {`{"when": "true", "restart": ["b.service"], "hint": {"text": "y"}}`, ""},
		"no hint":       {`{"when": "true", "restart": ["a.service"]}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Read(write(t, `{"a": {"when": "true", "restart": ["a.service"], "hint": {"text": "x", "file": "/run/x"}}, "b": `+tc.b+`}`))
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
