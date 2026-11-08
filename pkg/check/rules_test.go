package check

import (
	"errors"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/rules"
)

// Rules refuses a change a rule refuses on the node's facts, with the rule it fails, and lets the
// same change pass on other facts; without facts, the rule does not run.
func TestRules(t *testing.T) {
	img := testImage(t, `true`)
	translate := func(text string) config.Redacted {
		docs, _, err := img.TranslateRedacted("n1", config.Redact(parse(t, text)))
		if err != nil {
			t.Fatal(err)
		}
		return docs
	}
	old, next := translate(base), translate(strings.Replace(base, "name: first", "name: second", 1))
	for _, frozen := range []bool{false, true} {
		err := img.Rules(rules.Input{Documents: next, Old: old, Node: "n1", Facts: &facts{values: map[string]any{"frozen": frozen}}})
		r, refused := errors.AsType[*Refusal](err)
		if refused != frozen || (refused && (r.Stage != StageRules || len(r.Failures) != 1)) {
			t.Errorf("frozen %v: %#v", frozen, err)
		}
	}
	if err := img.Rules(rules.Input{Documents: next, Old: old, Node: "n1"}); err != nil {
		t.Errorf("without facts: %v", err)
	}
}
