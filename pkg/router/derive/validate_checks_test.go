package derive

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// validated runs the validator on docs, by kind, as one JSON array in the order of Kinds, and
// returns its refusals as chalkd reports them.
func validated(t *testing.T, docs map[string][]any) ([]string, error) {
	t.Helper()
	all := []any{}
	for _, kind := range Kinds() {
		all = append(all, docs[kind]...)
	}
	data, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := Validate(data)
	var lines []string
	for _, r := range rs {
		lines = append(lines, r.String())
	}
	return lines, err
}

// refused wants the validator to refuse docs with a refusal that holds want, or to fail with an
// error that holds it, as a guard does.
func refused(t *testing.T, docs map[string][]any, want string) {
	t.Helper()
	rs, err := validated(t, docs)
	if err != nil {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want %q", err, want)
		}
		return
	}
	if !slices.ContainsFunc(rs, func(r string) bool { return strings.Contains(r, want) }) {
		t.Errorf("refusals = %q, want %q", rs, want)
	}
}
