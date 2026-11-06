package refusals

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Parse takes nothing, or one report, and returns each refusal once, in the order printed; Encode
// prints what Parse reads.
func TestParse(t *testing.T) {
	for _, empty := range []string{"", "\n", `{"refusals": []}`} {
		if rs, err := Parse([]byte(empty)); err != nil || rs != nil {
			t.Errorf("%q = %v, %v", empty, rs, err)
		}
	}
	a := Refusal{Kind: "BGPPeer", Name: "fabric", Path: "spec.interfaces[0]", Message: "BGPPeer again and BGPPeer fabric both name enp0s3"}
	b := Refusal{Kind: "NAT64", Name: "lab.v6", Path: "spec.localZones[1].records[0]", Message: `"x": a quoted value`}
	data, err := Encode([]Refusal{a, b, a})
	if err != nil {
		t.Fatal(err)
	}
	rs, err := Parse(data)
	if err != nil || !slices.Equal(rs, []Refusal{a, b}) {
		t.Errorf("Parse(%s) = %v, %v", data, rs, err)
	}
	if got := a.String(); got != "BGPPeer/fabric: spec.interfaces[0]: BGPPeer again and BGPPeer fabric both name enp0s3" {
		t.Errorf("String = %q", got)
	}
	if empty, _ := Encode(nil); string(empty) != "{\"refusals\":[]}\n" {
		t.Errorf("Encode(nil) = %q", empty)
	}
}

// A report outside the format is an error of the program.
func TestParseRefuses(t *testing.T) {
	ok := `{"kind": "Demo", "name": "one", "path": "spec.name", "message": "taken"}`
	for name, c := range map[string]struct{ data, want string }{
		"no JSON":          {"refuse Demo/one: spec.name: taken", "it is no report of refusals"},
		"two reports":      {`{"refusals": []} {"refusals": []}`, "it holds more after its report of refusals"},
		"a closing brace":  {`{"refusals": []}}`, "it holds more after its report of refusals"},
		"null":             {`null`, "it is no report of refusals"},
		"no refusals":      {`{}`, "it is no report of refusals"},
		"null refusals":    {`{"refusals": null}`, "it is no report of refusals"},
		"a key's case":     {`{"Refusals": []}`, "it is no report of refusals"},
		"a key twice":      {`{"refusals": [], "refusals": []}`, "it is no report of refusals"},
		"a field's case":   {`{"refusals": [{"Kind": "Demo", "name": "one", "path": "spec.name", "message": "taken"}]}`, "refusal 1: it is no refusal"},
		"a missing field":  {`{"refusals": [{"kind": "Demo", "name": "one", "path": "spec.name"}]}`, "refusal 1: it is no refusal"},
		"a number":         {`{"refusals": [{"kind": "Demo", "name": "one", "path": "spec.name", "message": 1}]}`, "refusal 1: it is no refusal"},
		"a null refusal":   {`{"refusals": [null]}`, "refusal 1: it is no refusal"},
		"an unknown field": {`{"refusals": [], "warnings": []}`, "it is no report of refusals"},
		"a lower kind":     {`{"refusals": [{"kind": "demo", "name": "one", "path": "spec.name", "message": "taken"}]}`, `refusal 1: kind "demo" is no kind`},
		"a bad name":       {`{"refusals": [` + ok + `, {"kind": "Demo", "name": "One", "path": "spec.name", "message": "taken"}]}`, `refusal 2: name "One" is no document name`},
		"a spaced path":    {`{"refusals": [{"kind": "Demo", "name": "one", "path": "spec name", "message": "taken"}]}`, `refusal 1: path "spec name" is no field's path`},
		"a colon path":     {`{"refusals": [{"kind": "Demo", "name": "one", "path": "spec:name", "message": "taken"}]}`, `refusal 1: path "spec:name" is no field's path`},
		"no message":       {`{"refusals": [{"kind": "Demo", "name": "one", "path": "spec.name", "message": " "}]}`, "refusal 1: the message is empty"},
		"a line break":     {`{"refusals": [{"kind": "Demo", "name": "one", "path": "spec.name", "message": "a\nb"}]}`, "refusal 1: the message holds a control character"},
		"an escape":        {`{"refusals": [{"kind": "Demo", "name": "one", "path": "spec.name", "message": "\u001b[2J"}]}`, "refusal 1: the message holds a control character"},
		"a C1 CSI":         {`{"refusals": [{"kind": "Demo", "name": "one", "path": "spec.name", "message": "\u009b2J"}]}`, "refusal 1: the message holds a control character"},
		"a bidi override":  {`{"refusals": [{"kind": "Demo", "name": "one", "path": "spec.name", "message": "a\u202eb"}]}`, "refusal 1: the message holds a control character"},
		"a long message":   {`{"refusals": [{"kind": "Demo", "name": "one", "path": "spec.name", "message": "` + strings.Repeat("x", MaxMessage+1) + `"}]}`, "refusal 1: the message holds more than 1024 bytes"},
		"too long":         {`{"refusals": [` + strings.Repeat(ok+",", MaxReport/len(ok)) + ok + `]}`, "it holds more than 65536 bytes"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(c.data)); err == nil || err.Error() != c.want {
				t.Errorf("err = %v, want %s", err, c.want)
			}
		})
	}
}

// Message reports each refusal once, after "the config is refused: ", as a rule's refusal reads.
func TestMessage(t *testing.T) {
	a := Refusal{Kind: "Network", Name: "50-lab", Path: "spec.match.name", Message: "one"}
	b := Refusal{Kind: "BGPPeer", Name: "fabric", Path: "spec.interfaces", Message: "two"}
	if got := Message([]Refusal{a, b, a}); got != "the config is refused: Network/50-lab: spec.match.name: one; BGPPeer/fabric: spec.interfaces: two" {
		t.Errorf("Message = %q", got)
	}
}

// Printable replaces C0 and C1 control characters, DEL and bidirectional controls with spaces and
// keeps other runes, letters outside ASCII among them.
func TestPrintable(t *testing.T) {
	if got := Printable("a\nb\x1b[2J\u009b\x7f\u202e\u2066\u00e9"); got != "a b [2J    \u00e9" {
		t.Errorf("Printable = %q", got)
	}
}

// A refusal of a whole document leaves out the path, and one of no document, such as a kind's
// rule, is its message alone. Where is what comes before the message.
func TestStringWithoutPath(t *testing.T) {
	if got := (Refusal{Kind: "Demo", Name: "one", Message: "taken"}).String(); got != "Demo/one: taken" {
		t.Errorf("without a path = %q", got)
	}
	if got := (Refusal{Kind: "Demo", Message: "at most one Demo"}).String(); got != "at most one Demo" {
		t.Errorf("without a document = %q", got)
	}
	if got := (Refusal{Kind: "Demo", Name: "one", Path: "spec.x"}).Where(); got != "Demo/one: spec.x" {
		t.Errorf("Where = %q", got)
	}
}

// Errorf is a refusal as an error, which reads as the refusal does and which errors.As finds
// through a wrapping error.
func TestErrorf(t *testing.T) {
	err := fmt.Errorf("render: %w", Errorf("VRF", "tenant", "spec.interfaces[0]", "%s covers the device of VRF %s", "t*", "tenant"))
	var e *Error
	if !errors.As(err, &e) || e.Refusal != (Refusal{Kind: "VRF", Name: "tenant", Path: "spec.interfaces[0]", Message: "t* covers the device of VRF tenant"}) {
		t.Fatalf("errors.As(%v) = %+v", err, e)
	}
	if want := "VRF/tenant: spec.interfaces[0]: t* covers the device of VRF tenant"; e.Error() != want {
		t.Errorf("Error() = %q, want %q", e.Error(), want)
	}
}

// Split finds every Error that errors.Join and %w wrap, each once and in order, and returns what
// else the tree holds, with the text around it, as errors of their own.
func TestSplit(t *testing.T) {
	a := Errorf("VRF", "a", "spec.table", "one")
	b := Errorf("VRF", "b", "spec.table", "two")
	internal := errors.New("internal error: a guard")
	err := errors.Join(a, fmt.Errorf("Network 20-x: %w", errors.Join(b, a)), nil, fmt.Errorf("render: %w", internal))
	rs, others := Split(err)
	if want := []Refusal{a.(*Error).Refusal, b.(*Error).Refusal}; !slices.Equal(rs, want) {
		t.Errorf("refusals = %v, want %v", rs, want)
	}
	if len(others) != 1 || others[0].Error() != "render: internal error: a guard" {
		t.Errorf("others = %v", others)
	}
	if rs, others := Split(nil); rs != nil || others != nil {
		t.Errorf("Split(nil) = %v, %v", rs, others)
	}
}
