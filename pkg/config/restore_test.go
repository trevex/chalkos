package config

import (
	"encoding/json"
	"maps"
	"reflect"
	"strings"
	"testing"
)

// A Secret value that is no string keeps its JSON type in Redact's form, with no content of its
// own, so a schema of strings refuses it as it refuses the value; Hash still hashes it, so the
// version of a set tells two such values apart.
func TestRedactKeepsTheTypeOfOtherValues(t *testing.T) {
	for value, want := range map[string]string{
		"12345":  `0`,
		"1.5":    `0`,
		"true":   `false`,
		"[a, b]": `[]`,
		"{a: b}": `{}`,
		"null":   `null`,
	} {
		docs := parseDocs(t, strings.Replace(withSecrets, "token: s3cret", "token: "+value, 1))
		data, err := json.Marshal(Redact(docs)[0].Object["spec"])
		if err != nil {
			t.Fatal(err)
		}
		if got := string(data); got != `{"data":{"token":`+want+`}}` {
			t.Errorf("%s redacts to %s, want the token %s", value, got, want)
		}
	}
	a := parseDocs(t, strings.Replace(withSecrets, "token: s3cret", "token: 1", 1))
	b := parseDocs(t, strings.Replace(withSecrets, "token: s3cret", "token: 2", 1))
	if mustHash(t, a) == mustHash(t, b) {
		t.Error("two numbers hash the same")
	}
}

// A number Go built, not parsed, hides as a parsed one does: as 0.
func TestRedactHidesAGoNumberAsZero(t *testing.T) {
	for _, v := range []any{1.5, 7, int64(7)} {
		got := hideValue(v)
		if got != json.Number("0") {
			t.Errorf("%#v hides as %#v, want 0", v, got)
		}
	}
}

// A Secret whose spec.data, or spec, is no object, which no schema accepts, still reaches no
// checker with its value: Redact hides it whole by its type's zero value, or a string's hash.
func TestRedactHidesDataThatIsNoObject(t *testing.T) {
	for in, want := range map[string]string{
		"spec: {data: s3cret}":   `{"data":"` + sum("s3cret") + `"}`,
		"spec: {data: [s3cret]}": `{"data":[]}`,
		"spec: {data: 12345}":    `{"data":0}`,
		"spec: s3cret":           `"` + sum("s3cret") + `"`,
		"spec: [s3cret]":         `[]`,
	} {
		docs := parseDocs(t, "apiVersion: chalkos.dev/v1alpha1\nkind: Secret\nmetadata: {name: s}\n"+in+"\n")
		data, err := json.Marshal(Redact(docs)[0].Object["spec"])
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Errorf("%s redacts to %s, want %s", in, data, want)
		}
	}
}

// FromJSON keeps a number as JSON writes it and refuses JSON that is no object.
func TestFromJSON(t *testing.T) {
	d, err := FromJSON([]byte(`{"apiVersion": "chalkos.dev/v1alpha1", "kind": "Host", "metadata": {"name": "h"}, "spec": {"n": 12345678901234567890}}`))
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != "Host" || d.Name != "h" || d.Object["spec"].(map[string]any)["n"] != json.Number("12345678901234567890") {
		t.Errorf("document = %+v", d)
	}
	for _, bad := range []string{`[]`, `null`, `{`} {
		if _, err := FromJSON([]byte(bad)); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

// Restore puts the delivered secret values in place of the hashes of documents a check gave back
// with their defaults, and refuses documents that are not the delivered ones: another secret
// value, a document that was not delivered, one twice and one missing.
func TestRestore(t *testing.T) {
	delivered := parseDocs(t, withSecrets)
	checked := Redact(delivered)
	// A default the check filled in, outside the secret values.
	checked[2].Object = map[string]any{"apiVersion": CoreAPI, "kind": "Host", "metadata": map[string]any{"name": "default"}, "spec": map[string]any{"hostname": "h1", "extra": "default"}}
	got, err := Restore(checked, delivered)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got[0].Object, delivered[0].Object) || !reflect.DeepEqual(got[1].Object, delivered[1].Object) || !reflect.DeepEqual(got[2].Object, checked[2].Object) {
		t.Errorf("restored %v", got)
	}
	other := parseDocs(t, strings.Replace(withSecrets, "token: s3cret", "token: other", 1))
	for name, tc := range map[string]struct {
		checked, delivered []Document
		want               string
	}{
		"another value":     {checked, other, `Secret "demo" holds other secret values than were delivered`},
		"not delivered":     {checked, delivered[1:], `Secret "demo" is no delivered document`},
		"twice":             {append([]Document{checked[0]}, checked...), delivered, `Secret "demo" is among the checked documents twice`},
		"missing":           {checked[1:], delivered, "documents are missing: 3 delivered, 2 checked"},
		"an embedded value": {checked, parseDocs(t, strings.Replace(withSecrets, "data: {k: dg==}", "data: {k: eA==}", 1)), `Bundle "apps" holds other secret values than were delivered`},
	} {
		if _, err := Restore(tc.checked, tc.delivered); err == nil || err.Error() != tc.want {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

// Restore restores the values of a Secret object in a list too, keeps a delivered Secret without
// data without one, and refuses a checked Secret that lost its spec.
func TestRestoreEdges(t *testing.T) {
	delivered := parseDocs(t, withSecrets)
	listed := parseDocs(t, strings.Replace(withSecrets, "data: {k: bGlzdA==}", "data: {k: b3RoZXI=}", 1))
	if _, err := Restore(Redact(delivered), listed); err == nil || err.Error() != `Bundle "apps" holds other secret values than were delivered` {
		t.Errorf("another value in a list: err = %v", err)
	}
	got, err := Restore(Redact(delivered), delivered)
	if err != nil || !reflect.DeepEqual(got[1].Object, delivered[1].Object) {
		t.Errorf("the Bundle with a list = %v, %v", got[1].Object, err)
	}

	empty := parseDocs(t, "apiVersion: chalkos.dev/v1alpha1\nkind: Secret\nmetadata: {name: s}\nspec: {}\n")
	got, err = Restore(Redact(empty), empty)
	if err != nil || !reflect.DeepEqual(got[0].Object, empty[0].Object) {
		t.Errorf("a Secret without data = %v, %v", got, err)
	}

	checked := Redact(delivered)
	obj := maps.Clone(checked[0].Object)
	delete(obj, "spec")
	checked[0].Object = obj
	if _, err := Restore(checked, delivered); err == nil || err.Error() != `Secret "demo" holds other secret values than were delivered` {
		t.Errorf("a checked Secret without spec: err = %v", err)
	}
}

// Restore takes the delivered values of the spec fields that embed a Secret object at any depth,
// and keeps the defaults a check filled into the other fields of the same spec.
func TestRestoreEmbeddedKeepsDefaults(t *testing.T) {
	delivered := parseDocs(t, `
apiVersion: other.example.org/v1alpha1
kind: Chart
metadata: {name: c}
spec:
  values: {deep: {secret: {apiVersion: v1, kind: Secret, metadata: {name: s}, data: {k: dg==}}}}
`)
	checked := Redact(delivered)
	obj := maps.Clone(checked[0].Object)
	spec := maps.Clone(obj["spec"].(map[string]any))
	spec["replicas"] = json.Number("1")
	obj["spec"] = spec
	checked[0].Object = obj
	got, err := Restore(checked, delivered)
	if err != nil {
		t.Fatal(err)
	}
	gotSpec := got[0].Object["spec"].(map[string]any)
	if !reflect.DeepEqual(gotSpec["values"], delivered[0].Object["spec"].(map[string]any)["values"]) || gotSpec["replicas"] != json.Number("1") {
		t.Errorf("restored %v", gotSpec)
	}
	if !Redacted(delivered[0], strings.Split("spec.values.deep.secret.data.k", ".")) || Redacted(delivered[0], strings.Split("spec.values.deep.secret.metadata.name", ".")) {
		t.Error("Redacted does not tell the embedded Secret's data")
	}
}
