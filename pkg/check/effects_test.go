package check

import (
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/effects"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/rules"
)

// Due names each effect a delivery takes with what fired it, an output whose state changed, a kind
// whose documents did or a rule that holds, and the outputs that changed; an effect nothing fired
// is left out. PlanOf adds each effect's actions.
func TestDue(t *testing.T) {
	s := testSchemas(t)
	translate := func(text string) config.Documents {
		tr, err := Translate("n1", config.Redact(parse(t, text)), s, config.Target{Platform: "metal"})
		if err != nil {
			t.Fatal(err)
		}
		return tr.Documents
	}
	when, err := rules.CompileEffect("named", `config["demo.example.org/v1alpha1"].Demo.one.spec.name != oldConfig["demo.example.org/v1alpha1"].Demo.one.spec.name`)
	if err != nil {
		t.Fatal(err)
	}
	fx := Effects{Defs: map[string]effects.Definition{
		"demo":  {Outputs: []string{"demo"}, Restart: []string{"demo.service"}},
		"disks": {Kinds: []string{"SystemDisk"}, Restart: []string{"disks.service"}},
		"names": {Kinds: []string{"Demo"}, Reload: []string{"names.service"}, Run: "/bin/true", Chalkd: "demo:names"},
		"named": {When: when.When, Restart: []string{"a.service", "b.service"}},
	}, Rules: []*rules.EffectRule{when}}
	old := translate(base)
	kinds, err := config.KindHashes(old, s, true)
	if err != nil {
		t.Fatal(err)
	}
	f := &facts{}
	olds := []Comparison{{Documents: old, Hashed: true, Facts: f, OldFacts: f, Outputs: map[string]outputs.State{"demo": {Files: "a"}}, Kinds: kinds}}
	d := Delivery{Node: "n1", Documents: translate(strings.Replace(base, "name: first", "name: second", 1)), Schemas: s, Outputs: map[string]outputs.State{"demo": {Files: "b"}}}
	o, warnings, err := Due(fx, d, olds)
	if err != nil || len(warnings) > 0 {
		t.Fatal(err, warnings)
	}
	want := Outcome{Outputs: []string{"demo"}, Effects: []Fired{
		{Name: "demo", By: []string{"output demo"}},
		{Name: "named", By: []string{"rule"}},
		{Name: "names", By: []string{"kind Demo"}},
	}}
	if !reflect.DeepEqual(o, want) {
		t.Errorf("outcome = %+v, want %+v", o, want)
	}
	plan := PlanOf(fx, o)
	if got := plan.Effects[2].Actions; !reflect.DeepEqual(got, []string{"reload names.service", "run /bin/true", "chalkd demo:names"}) {
		t.Errorf("names does %v", got)
	}
	if got := plan.Effects[1].Actions; !reflect.DeepEqual(got, []string{"restart a.service b.service"}) {
		t.Errorf("named does %v", got)
	}
}
