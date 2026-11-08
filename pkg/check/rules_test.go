package check

import (
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/rules"
)

// Rules refuses a change a rule refuses on the node's facts, and lets the same change pass on
// other facts.
func TestRules(t *testing.T) {
	s := testSchemas(t)
	set, err := rules.Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	translate := func(text string) config.Redacted {
		docs, _, err := config.TranslateRedacted("n1", config.Redact(parse(t, text)), s, config.Target{Platform: "metal"})
		if err != nil {
			t.Fatal(err)
		}
		return docs
	}
	old, next := translate(base), translate(strings.Replace(base, "name: first", "name: second", 1))
	for _, frozen := range []bool{false, true} {
		failures, err := Rules(set, "n1", next, old, &facts{values: map[string]any{"frozen": frozen}})
		if err != nil || (len(failures) == 1) != frozen {
			t.Errorf("frozen %v: failures %v, %v", frozen, failures, err)
		}
	}
}
