package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/router/derive"
)

// A number of an integer value takes its integer spelling wherever it is; a fraction, plain
// digits, a string and a number whose exponent would spell it in too many digits stay as written.
func TestIntegral(t *testing.T) {
	n := func(s string) json.Number { return json.Number(s) }
	for _, c := range []struct {
		name     string
		in, want any
	}{
		{"a fraction of zero", n("100.0"), n("100")},
		{"an exponent", n("1e2"), n("100")},
		{"a signed exponent", n("1.00E+2"), n("100")},
		{"a negative", n("-1e2"), n("-100")},
		{"a negative exponent", n("1500e-1"), n("150")},
		{"a fraction", n("1.5"), n("1.5")},
		{"a fraction by its exponent", n("15e-1"), n("15e-1")},
		{"plain digits", n("100"), n("100")},
		{"a large exponent", n("1e400"), n("1" + strings.Repeat("0", 400))},
		{"a huge exponent", n("1e100000"), n("1e100000")},
		{"a string", "100.0", "100.0"},
		{"nested", map[string]any{"a": []any{n("1e2"), map[string]any{"b": n("2.0"), "c": "3.0"}}, "d": true},
			map[string]any{"a": []any{n("100"), map[string]any{"b": n("2"), "c": "3.0"}}, "d": true}},
	} {
		if got := integral(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: integral(%v) = %#v, want %#v", c.name, c.in, got, c.want)
		}
	}
}

// A VRF's table written 100.0 or 1e2 is 100 in the set, and a document read back from a slot's
// JSON is too.
func TestSetHoldsIntegersInPlainDigits(t *testing.T) {
	s := routerImageSchemas(t)
	for _, table := range []string{"100.0", "1e2"} {
		set, err := NewSet(mustParse(t, minimal+routerDoc("VRF", "tenant", `{table: `+table+`}`)), s)
		if err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
		vrf := set.Documents().Of(derive.API, "VRF")
		if len(vrf) != 1 {
			t.Fatalf("table %s: %d VRFs", table, len(vrf))
		}
		if got := vrf[0].Object["spec"].(map[string]any)["table"]; got != json.Number("100") {
			t.Errorf("table %s: stored %#v, want 100", table, got)
		}
	}
	d, err := FromJSON([]byte(`{"apiVersion": "router.chalkos.dev/v1alpha1", "kind": "VRF", "metadata": {"name": "tenant"}, "spec": {"table": 1.00e2}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Object["spec"].(map[string]any)["table"]; got != json.Number("100") {
		t.Errorf("FromJSON: table %#v, want 100", got)
	}
}
