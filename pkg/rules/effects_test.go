package rules

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
)

func effect(t *testing.T, when string) *Effect {
	t.Helper()
	e, err := CompileEffect("demo", Restart, when)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// dueUnits evaluates the effects and names the unit and action of each that is due.
func dueUnits(effects []*Effect, in EffectInput) ([]string, []error) {
	due, errs := Due(effects, in)
	var out []string
	for _, e := range due {
		out = append(out, e.Where())
	}
	return out, errs
}

// An effect sees the kinds that changed, the config before and after the delivery with its
// Secrets hashed, and the facts before and after it.
func TestEffectVariables(t *testing.T) {
	secret := func(value string) config.Document {
		return config.FromObject(map[string]any{
			"apiVersion": "chalkos.dev/v1alpha1", "kind": "Secret",
			"metadata": map[string]any{"name": "s"}, "spec": map[string]any{"data": map[string]any{"token": value}},
		})
	}
	old := []config.Document{doc(t, "Demo", "a", `{"replicas": 1}`), secret("s3cret")}
	next := []config.Document{doc(t, "Demo", "a", `{"replicas": 2}`), secret("s3cret")}
	for name, tc := range map[string]struct {
		when string
		due  bool
	}{
		"a changed kind":      {`"Demo" in changedKinds`, true},
		"an unchanged kind":   {`"Other" in changedKinds`, false},
		"config":              {`config["t.example.org/v1"].Demo.a.spec.replicas == 2`, true},
		"oldConfig":           {`oldConfig["t.example.org/v1"].Demo.a.spec.replicas == 1`, true},
		"a field compared":    {`config["t.example.org/v1"].Demo.a.spec != oldConfig["t.example.org/v1"].Demo.a.spec`, true},
		"a hashed Secret":     {`config["chalkos.dev/v1alpha1"].Secret.s.spec.data.token == "s3cret"`, false},
		"the same Secret":     {`config["chalkos.dev/v1alpha1"].Secret == oldConfig["chalkos.dev/v1alpha1"].Secret`, true},
		"facts after":         {`facts.share.kind == "worker"`, true},
		"facts before":        {`oldFacts.share == null`, true},
		"a fact that differs": {`facts.share != oldFacts.share`, true},
	} {
		t.Run(name, func(t *testing.T) {
			in := EffectInput{
				Documents: next, Old: old, ChangedKinds: []string{"Demo"},
				Facts:    &fakeFacts{values: map[string]any{"share": map[string]any{"kind": "worker"}}},
				OldFacts: &fakeFacts{},
			}
			got, errs := dueUnits([]*Effect{effect(t, tc.when)}, in)
			if len(errs) > 0 || (len(got) == 1) != tc.due {
				t.Errorf("due = %v, %v; want %v", got, errs, tc.due)
			}
		})
	}
}

// An effect loads a fact when it reads it, from the facts after the delivery or from those before
// it, so one whose first operand of || holds loads none.
func TestEffectLoadsFactsLazily(t *testing.T) {
	e := effect(t, `"Demo" in changedKinds || facts.share != oldFacts.share`)
	if !slices.Equal(e.Facts, []string{"share"}) {
		t.Errorf("facts = %v", e.Facts)
	}
	after, before := &fakeFacts{}, &fakeFacts{}
	if got, errs := dueUnits([]*Effect{e}, EffectInput{ChangedKinds: []string{"Demo"}, Facts: after, OldFacts: before}); len(errs) > 0 || len(got) != 1 {
		t.Errorf("due = %v, %v", got, errs)
	}
	if after.loads["share"] != 0 || before.loads["share"] != 0 {
		t.Errorf("loaded share %d times after and %d before, want neither", after.loads["share"], before.loads["share"])
	}
	if got, errs := dueUnits([]*Effect{e}, EffectInput{Facts: after, OldFacts: before}); len(errs) > 0 || len(got) != 0 {
		t.Errorf("due = %v, %v", got, errs)
	}
	if after.loads["share"] != 1 || before.loads["share"] != 1 {
		t.Errorf("loaded share %d times after and %d before, want once each", after.loads["share"], before.loads["share"])
	}
}

// An effect that cannot be evaluated is due, and says why without the values it read: a unit
// restarts once more than it needed to rather than run on config it has not read.
func TestEffectThatFailsIsDue(t *testing.T) {
	effects := []*Effect{
		effect(t, `config["t.example.org/v1"].Demo.a.spec.missing == "s3cret"`),
		effect(t, `facts.share == null`),
		effect(t, `config["t.example.org/v1"].Demo.a.spec.name`),
		effect(t, `false`),
	}
	in := EffectInput{
		Documents: []config.Document{doc(t, "Demo", "a", `{"name": "s3cret"}`)},
		Facts:     &fakeFacts{errs: map[string]error{"share": errors.New("not JSON")}},
	}
	got, errs := dueUnits(effects, in)
	if !slices.Equal(got, []string{"demo restart", "demo restart", "demo restart"}) {
		t.Errorf("due = %v", got)
	}
	want := []string{
		"demo restart: a rule cannot be evaluated: no such key",
		"demo restart: cannot check: fact share: not JSON",
		"demo restart: a rule yields string, not a bool",
	}
	var msgs []string
	for _, err := range errs {
		msgs = append(msgs, err.Error())
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("err = %v shows a value", err)
		}
	}
	if !slices.Equal(msgs, want) {
		t.Errorf("errs = %q, want %q", msgs, want)
	}
}

func TestCompileEffectErrors(t *testing.T) {
	for name, tc := range map[string]struct{ when, want string }{
		"syntax":            {"changedKinds ==", "Syntax error"},
		"not a bool":        {"size(changedKinds)", "yields int, not bool"},
		"a kind's variable": {"size(documents) > 0", "undeclared reference to 'documents'"},
		"a field's":         {"self == oldSelf", "undeclared reference to 'self'"},
		"facts by index":    {`facts["share"] == null`, "facts are read by name, as facts.<name>"},
		"oldFacts alone":    {`oldFacts == null`, "oldFacts are read by name, as oldFacts.<name>"},
		"has":               {`has(oldFacts.share)`, "has() does not apply to oldFacts"},
		"empty":             {" ", "the rule is empty"},
		"costly":            {`changedKinds.all(a, changedKinds.all(b, changedKinds.all(c, a + b + c != "")))`, "more than the limit"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := CompileEffect("demo", Reload, tc.when)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.HasPrefix(err.Error(), "demo reload: rule ") {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// An effect compares the config with an old one whose secret values are hashed already, as a
// delivery that failed part way recorded it, as with the old config as delivered.
func TestEffectOldHashed(t *testing.T) {
	secret := config.FromObject(map[string]any{
		"apiVersion": "chalkos.dev/v1alpha1", "kind": "Secret",
		"metadata": map[string]any{"name": "s"}, "spec": map[string]any{"data": map[string]any{"token": "s3cret"}},
	})
	e := effect(t, `config["chalkos.dev/v1alpha1"].Secret == oldConfig["chalkos.dev/v1alpha1"].Secret`)
	for name, in := range map[string]EffectInput{
		"as delivered": {Documents: []config.Document{secret}, Old: []config.Document{secret}},
		"hashed":       {Documents: []config.Document{secret}, Old: config.Redact([]config.Document{secret}), OldHashed: true},
	} {
		if got, errs := dueUnits([]*Effect{e}, in); len(errs) > 0 || len(got) != 1 {
			t.Errorf("%s: due = %v, %v; want the same Secret", name, got, errs)
		}
	}
	in := EffectInput{Documents: []config.Document{secret}, Old: config.Redact([]config.Document{secret})}
	if got, errs := dueUnits([]*Effect{e}, in); len(errs) > 0 || len(got) != 0 {
		t.Errorf("hashed twice: due = %v, %v; want another Secret", got, errs)
	}
}
