package check

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/effects"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/rules"
)

// script writes an executable shell script of the commands and returns its path.
func script(t *testing.T, commands string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -e\n"+commands+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// testImage checks config with testSchemas, an output demo of the Demo documents that render
// writes, the effect demo on that output and the effect names on the Demo kind.
func testImage(t *testing.T, render string) Image {
	t.Helper()
	s := testSchemas(t)
	set, err := rules.Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	return Image{
		Schemas: s,
		Target:  config.Target{Platform: "metal"},
		Rules:   set,
		Effects: Effects{Defs: map[string]effects.Definition{
			"demo":  {Outputs: []string{"demo"}, Restart: []string{"demo.service"}},
			"names": {Kinds: []string{"Demo"}, Reload: []string{"names.service"}},
		}},
		Outputs: map[string]outputs.Definition{"demo": {
			Inputs: []string{"Demo"}, Render: script(t, render), Dir: filepath.Join(root, "demo"),
			User: strconv.Itoa(os.Getuid()), Group: strconv.Itoa(os.Getgid()), Mode: "0644",
		}},
		Render: outputs.Renderer{Root: root, Run: outputs.Direct{}},
	}
}

// running is what a node that runs base gives a check: its documents, hashed, and the state it
// compares with, as Run of base on img leaves it.
func running(t *testing.T, img Image, f rules.Facts) Input {
	t.Helper()
	res, err := Run(context.Background(), img, Input{Node: "n1", Delivered: config.Redact(parse(t, base)), Facts: f})
	if err != nil {
		t.Fatal(err)
	}
	res.Staged.Discard()
	kinds, err := config.KindHashes(res.Documents, img.Schemas, true)
	if err != nil {
		t.Fatal(err)
	}
	return Input{Node: "n1", Old: res.Documents, Facts: f, Compare: []Comparison{{
		Documents: res.Documents, Hashed: true, Facts: f, OldFacts: f, Outputs: res.Staged.States(), Kinds: kinds,
	}}}
}

// Run plans the effects a config fires against the config the node runs, with what fired each,
// and the outputs it changes; the config the node runs fires none.
func TestRunPlansTheEffects(t *testing.T) {
	img := testImage(t, `cat > "$OUT/demo.json"`)
	in := running(t, img, &facts{})
	in.Delivered = config.Redact(parse(t, strings.Replace(base, "name: first", "name: second", 1)))
	res, err := Run(context.Background(), img, in)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Staged.Discard()
	want := Plan{Outputs: []string{"demo"}, Effects: []PlannedEffect{
		{Name: "demo", FiredBy: []string{"output demo"}, Actions: []string{"restart demo.service"}},
		{Name: "names", FiredBy: []string{"kind Demo"}, Actions: []string{"reload names.service"}},
	}}
	if !reflect.DeepEqual(res.Plan, want) {
		t.Errorf("plan = %+v, want %+v", res.Plan, want)
	}
	in.Delivered = config.Redact(parse(t, base))
	if res, err := Run(context.Background(), img, in); err != nil || len(res.Plan.Outputs) != 0 || len(res.Plan.Effects) != 0 {
		t.Errorf("the running config plans %+v: %v", res.Plan, err)
	}
}

// An effect whose rule cannot be evaluated is planned all the same, and Run warns of the rule as
// a delivery does.
func TestRunWarnsOfARuleItCannotEvaluate(t *testing.T) {
	img := testImage(t, `cat > "$OUT/demo.json"`)
	when, err := rules.CompileEffect("broken", `config["demo.example.org/v1alpha1"].Demo.missing.spec.name == "x"`)
	if err != nil {
		t.Fatal(err)
	}
	img.Effects.Defs["broken"] = effects.Definition{When: when.When, Restart: []string{"broken.service"}}
	img.Effects.Rules = []*rules.EffectRule{when}
	in := running(t, img, &facts{})
	in.Delivered = config.Redact(parse(t, base))
	res, err := Run(context.Background(), img, in)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Staged.Discard()
	want := Plan{Effects: []PlannedEffect{{Name: "broken", FiredBy: []string{"rule"}, Actions: []string{"restart broken.service"}}}}
	if !reflect.DeepEqual(res.Plan, want) {
		t.Errorf("plan = %+v, want %+v", res.Plan, want)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "broken") || !strings.HasSuffix(res.Warnings[0], "taking the effect all the same") {
		t.Errorf("warnings = %q", res.Warnings)
	}
}

// A rotated Secret changes the state of the output whose documents reference it, though its files
// stay the same, as the node compares the values: the hashes Run reads stand for them.
func TestRunPlansARotatedSecret(t *testing.T) {
	img := testImage(t, `cat > "$OUT/demo.json"`)
	in := running(t, img, &facts{})
	rotated := strings.Replace(base, "token: s3cret", "token: n3w", 1)
	in.Delivered = config.Redact(parse(t, rotated))
	res, err := Run(context.Background(), img, in)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Staged.Discard()
	if !slices.Equal(res.Plan.Outputs, []string{"demo"}) || len(res.Plan.Effects) != 2 {
		t.Errorf("plan = %+v", res.Plan)
	}
	live, err := outputs.Live(outputs.Definition{Inputs: []string{"Demo"}, Dir: t.TempDir()}, outputs.Input{Documents: parse(t, rotated), Schemas: img.Schemas})
	if err != nil || live.Secrets != res.Staged.States()["demo"].Secrets {
		t.Errorf("the hashed input's Secrets state %q differs from the values' %q: %v", res.Staged.States()["demo"].Secrets, live.Secrets, err)
	}
}

// The renderers read the documents of their kinds without a Secret's value, and the documents
// Run returns hold the value's hash.
func TestRunReadsNoSecretValue(t *testing.T) {
	img := testImage(t, `cat > "$OUT/demo.json"`)
	res, err := Run(context.Background(), img, Input{Node: "n1", Delivered: config.Redact(parse(t, base))})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Staged.Discard()
	sum := sha256.Sum256([]byte("s3cret"))
	i := slices.IndexFunc(res.Documents, func(d config.Document) bool { return d.IsSecret() })
	if i < 0 || res.Documents[i].Object["spec"].(map[string]any)["data"].(map[string]any)["token"] != hex.EncodeToString(sum[:]) {
		t.Errorf("documents = %v", res.Documents)
	}
	if err := res.Staged.Swap(); err != nil {
		t.Fatal(err)
	}
	rendered, err := os.ReadFile(filepath.Join(img.Outputs["demo"].Dir, "demo.json"))
	if err != nil || strings.Contains(string(rendered), "s3cret") || !strings.Contains(string(rendered), `"name":"first"`) {
		t.Errorf("the renderer read %s: %v", rendered, err)
	}
}

// Run refuses a config at the stage that refuses it, with the message a delivery's refusal gives;
// a fact a rule reads that cannot be loaded refuses it at the rules, as chalkd's evaluate does.
func TestRunRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		render, next   string
		frozen, broken bool
		stage          Stage
		want           string
	}{
		"the set":      {`cat > "$OUT/demo.json"`, base + "---\napiVersion: demo.example.org/v1alpha1\nkind: Demo\nmetadata: {name: one}\nspec: {name: twice}\n", false, false, StageConfig, `the config: two Demo documents named "one"`},
		"a rule":       {`cat > "$OUT/demo.json"`, strings.Replace(base, "name: first", "name: second", 1), true, false, StageRules, `the config is refused: Demo "one" spec.name: the name stays while the demo is frozen`},
		"a renderer":   {`exit 3`, base, false, false, StageOutputs, "output demo: its renderer failed (exit 3); see the node's journal"},
		"an unchanged": {`cat > "$OUT/demo.json"`, base, true, false, "", ""},
		"a fact":       {`cat > "$OUT/demo.json"`, strings.Replace(base, "name: first", "name: second", 1), false, true, StageRules, "cannot check: fact frozen: share.json is not JSON"},
	} {
		t.Run(name, func(t *testing.T) {
			img := testImage(t, `cat > "$OUT/demo.json"`)
			f := &facts{values: map[string]any{"frozen": tc.frozen}}
			in := running(t, img, f)
			if tc.broken {
				in.Facts = &facts{err: errors.New("share.json is not JSON")}
			}
			demo := img.Outputs["demo"]
			demo.Render = script(t, tc.render)
			img.Outputs["demo"] = demo
			in.Delivered = config.Redact(parse(t, tc.next))
			res, err := Run(context.Background(), img, in)
			var r *Refusal
			switch {
			case tc.stage == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.stage == "":
				res.Staged.Discard()
			case !errors.As(err, &r) || r.Stage != tc.stage || r.Message != tc.want:
				t.Errorf("err = %#v, want %s: %s", err, tc.stage, tc.want)
			}
		})
	}
}

// The facts source loads a fact when a rule reads it: the rule of a Demo's name, which runs where
// the running config has the Demo, reads frozen, which a config without a Demo never asks for.
func TestRunAsksForTheFactsRulesRead(t *testing.T) {
	img := testImage(t, `cat > "$OUT/demo.json"`)
	f := &facts{}
	in := running(t, img, f)
	for _, tc := range []struct {
		next  string
		asked bool
	}{{base, true}, {strings.SplitN(base, "---", 2)[0], false}} {
		f.asked = nil
		in.Delivered = config.Redact(parse(t, tc.next))
		res, err := Run(context.Background(), img, in)
		if err != nil {
			t.Fatal(err)
		}
		res.Staged.Discard()
		if slices.Contains(f.asked, "frozen") != tc.asked {
			t.Errorf("a config of %d documents asked for %v", len(in.Delivered), f.asked)
		}
	}
}
