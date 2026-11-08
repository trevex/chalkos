package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const builtin = "../../test/fixtures/kinds/builtin"

// newDir lays out a contract directory: the built-in kinds' schemas and the files given, by name.
func newDir(t testing.TB, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(filepath.Join(dir, SchemasDir), os.DirFS(builtin)); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoad(t *testing.T) {
	effects, err := os.ReadFile("../../test/fixtures/kinds/effects.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := newDir(t, map[string]string{
		FactsFile:       `{"cluster": {"file": "/state/kubernetes/cluster.json"}, "share": {"file": "/state/kubernetes/share.json"}}`,
		EffectsFile:     string(effects),
		ServicesFile:    `{"demo.service": {"kinds": ["TimeSync"]}}`,
		OutputsFile:     `{"demo": {"inputs": ["TimeSync"], "render": "/nix/store/x-demo/bin/render", "dir": "/run/demo", "user": "root", "group": "root", "mode": "0644"}}`,
		ValidatorsFile:  `{"demo": {"inputs": ["TimeSync"], "program": "/nix/store/x-demo/bin/validate"}}`,
		DiagnosticsFile: `{"links": {"description": "The node's links.", "command": ["/nix/store/x-iproute2/bin/ip", "-json", "link"], "role": "reader", "timeout": "10s"}}`,
	})
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if set, err := c.Rules(); c.Dir != dir || c.Schemas == nil || err != nil || len(set.Programs) == 0 {
		t.Errorf("the schemas and their rules: %+v, %v", c, err)
	}
	if len(c.Facts) != 2 || len(c.Services) != 1 || len(c.Outputs) != 1 || len(c.Validators) != 1 || len(c.Diagnostics) != 1 {
		t.Errorf("the definitions: %+v", c)
	}
	compiled, err := c.EffectRules()
	if err != nil || len(c.Effects) == 0 || len(compiled) == 0 {
		t.Fatalf("effects %d, effect rules %d, %v", len(c.Effects), len(compiled), err)
	}
	for i := 1; i < len(compiled); i++ {
		if compiled[i-1].Name >= compiled[i].Name {
			t.Errorf("the effect rules are not in the order of their names: %s, %s", compiled[i-1].Name, compiled[i].Name)
		}
	}
}

// TestLoadWithoutDefinitions loads a directory of schemas alone: every other file declares none.
func TestLoadWithoutDefinitions(t *testing.T) {
	c, err := Load(newDir(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Facts == nil || c.Effects == nil || c.Services == nil || c.Outputs == nil || c.Validators == nil || c.Diagnostics == nil {
		t.Errorf("a contract of schemas alone: %+v", c)
	}
	if _, err := Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "the image's schemas: ") {
		t.Errorf("a directory without schemas: %v", err)
	}
}

// TestLoadRefuses reports every file whose definitions chalkd could not carry out, facts among
// them.
func TestLoadRefuses(t *testing.T) {
	dir := newDir(t, map[string]string{
		FactsFile:       `{"probe": {"command": "probe"}}`,
		OutputsFile:     `{"demo": {"inputs": ["TimeSync"], "render": "/nix/store/x-demo/bin/render", "dir": "/run/chalkos/other", "user": "root", "group": "root", "mode": "0644"}}`,
		ValidatorsFile:  `{"demo": {"inputs": [], "program": "/nix/store/x-demo/bin/validate"}}`,
		DiagnosticsFile: `{"Links": {"description": "The node's links.", "command": ["/nix/store/x-iproute2/bin/ip"], "role": "reader", "timeout": "10s"}}`,
		ServicesFile:    `[]`,
	})
	_, err := Load(dir)
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{
		filepath.Join(dir, FactsFile) + ": fact probe: ",
		filepath.Join(dir, OutputsFile) + ": output demo: ",
		filepath.Join(dir, ValidatorsFile) + ": validator demo: ",
		filepath.Join(dir, DiagnosticsFile) + ": diagnostic Links: ",
		filepath.Join(dir, ServicesFile) + ": ",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v\nwant one with %q", err, want)
		}
	}
}

// TestRulesThatDoNotCompile loads a contract whose rules do not compile, and refuses them once
// they are asked for.
func TestRulesThatDoNotCompile(t *testing.T) {
	dir := newDir(t, map[string]string{EffectsFile: `{"demo": {"when": "config ==", "restart": ["demo.service"]}}`})
	name := filepath.Join(dir, SchemasDir, "time.chalkos.dev_v1alpha1_TimeSync.json")
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var sch map[string]any
	if err := json.Unmarshal(raw, &sch); err != nil {
		t.Fatal(err)
	}
	sch["x-chalkos-kind"].(map[string]any)["rules"] = []any{map[string]any{"rule": "documents =="}}
	raw, _ = json.Marshal(sch)
	if err := os.WriteFile(name, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Rules(); err == nil || !strings.Contains(err.Error(), "TimeSync: rule ") {
		t.Errorf("Rules = %v", err)
	}
	if _, err := c.EffectRules(); err == nil || !strings.Contains(err.Error(), `effect demo: rule "config =="`) {
		t.Errorf("EffectRules = %v", err)
	}
}

// TestLoadRenderRoot checks the outputs' dirs against the root a node renders them below.
func TestLoadRenderRoot(t *testing.T) {
	dir := newDir(t, map[string]string{
		OutputsFile: `{"demo": {"inputs": ["TimeSync"], "render": "/nix/store/x-demo/bin/render", "dir": "/tmp/run/chalkos/outputs/demo", "user": "root", "group": "root", "mode": "0644"}}`,
	})
	if _, err := Load(dir); err == nil {
		t.Error("an output's dir outside /run: no error")
	}
	if _, err := Load(dir, RenderRoot("/tmp/run/chalkos/outputs")); err != nil {
		t.Error(err)
	}
}

func TestFromSchemas(t *testing.T) {
	entries, err := os.ReadDir(builtin)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if files[e.Name()], err = os.ReadFile(filepath.Join(builtin, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	c, err := FromSchemas(files)
	if err != nil {
		t.Fatal(err)
	}
	if set, err := c.Rules(); c.Schemas == nil || err != nil || len(set.Programs) == 0 {
		t.Errorf("FromSchemas = %+v, %v", c, err)
	}
	if _, err := FromSchemas(map[string][]byte{"x.yaml": nil}); err == nil {
		t.Error("a file that is no schema: no error")
	}
}
