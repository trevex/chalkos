package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"reflect"
	"strings"
	"testing"
)

const hashed = `
apiVersion: chalkos.dev/v1alpha1
kind: Host
metadata: {name: default}
spec: {hostname: n1}
---
apiVersion: demo.example.org/v1alpha1
kind: Demo
metadata: {name: b}
spec: {name: two}
---
apiVersion: demo.example.org/v1alpha1
kind: Demo
metadata: {name: a}
spec: {name: one, token: {secretRef: {name: demo, key: token}}}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: demo}
spec: {data: {token: s3cret}}
`

// reordered holds the documents of hashed in another order and layout.
const reordered = `
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata:
  name: demo
spec:
  data:
    token: s3cret
---
apiVersion: demo.example.org/v1alpha1
kind: Demo
metadata: {name: a}
spec:
  token:
    secretRef: {key: token, name: demo}
  name: one
---
spec: {name: two}
kind: Demo
apiVersion: demo.example.org/v1alpha1
metadata: {name: b}
---
apiVersion: chalkos.dev/v1alpha1
kind: Host
metadata: {name: default}
spec: {hostname: n1}
`

func hashOf(t *testing.T, yaml string) string {
	t.Helper()
	h, err := Hash(parseDocs(t, yaml))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHashIgnoresOrderAndLayout(t *testing.T) {
	if a, b := hashOf(t, hashed), hashOf(t, reordered); a != b {
		t.Errorf("hashes differ: %s, %s", a, b)
	}
	if h := hashOf(t, hashed); len(h) != 64 || strings.ToLower(h) != h {
		t.Errorf("hash = %q, want 64 lower-case hex digits", h)
	}
}

func TestHashChangesWithAValue(t *testing.T) {
	for _, edit := range []struct{ from, to string }{
		{"name: two", "name: three"},
		{"token: s3cret", "token: other"},
		{"metadata: {name: b}", "metadata: {name: c}"},
	} {
		if hashOf(t, hashed) == hashOf(t, strings.Replace(hashed, edit.from, edit.to, 1)) {
			t.Errorf("%s -> %s left the hash unchanged", edit.from, edit.to)
		}
	}
	// Numbers are hashed as FromObject keeps them: an integer in plain digits, a fraction as written.
	replicas := func(n string) string {
		return hashOf(t, strings.Replace(hashed, "name: two", "name: two, replicas: "+n, 1))
	}
	if replicas("1") != replicas("1.0") {
		t.Error("1 and 1.0 hash apart")
	}
	if replicas("1.5") == replicas("1.50") {
		t.Error("1.5 and 1.50 hash alike")
	}
}

func TestHashHidesSecretValues(t *testing.T) {
	c, err := json.Marshal(Redact(parseDocs(t, hashed)).Objects())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("s3cret"))
	if bytes.Contains(c, []byte("s3cret")) || !bytes.Contains(c, []byte(hex.EncodeToString(sum[:]))) {
		t.Errorf("canonical form = %s, want the secret's SHA-256 in place of its value", c)
	}
}

// The Demo fixture's token, a secretRef as the converter writes one, is a key reference.
func TestRefsOfASecretRef(t *testing.T) {
	docs := parseDocs(t, hashed)
	s := imageSchemas(t)
	for _, d := range docs {
		got := Refs(d, s)
		var want []Ref
		if d.Kind == "Demo" && d.Name == "a" {
			want = []Ref{{Path: "spec.token", Target: ID{APIVersion: CoreAPI, Kind: "Secret", Name: "demo"}, Key: "token"}}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: refs = %v, want %v", d, got, want)
		}
	}
}

func TestKindHashes(t *testing.T) {
	s := imageSchemas(t)
	hashes := func(yaml string) map[string]string {
		t.Helper()
		h, err := KindHashes(Redact(parseDocs(t, yaml)), s)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	before := hashes(hashed)
	if _, ok := before["Secret"]; ok || len(before) != 2 {
		t.Errorf("kinds = %v, want Demo and Host alone", before)
	}
	secret := hashes(strings.Replace(hashed, "token: s3cret", "token: other", 1))
	if secret["Demo"] == before["Demo"] || secret["Host"] != before["Host"] {
		t.Errorf("a referenced Secret's change: %v -> %v, want Demo alone to change", before, secret)
	}
	demo := hashes(strings.Replace(hashed, "name: two", "name: three", 1))
	if demo["Demo"] == before["Demo"] || demo["Host"] != before["Host"] {
		t.Errorf("a Demo's change: %v -> %v, want Demo alone to change", before, demo)
	}
}

func TestKindHashesIgnoreAnUnreferencedSecret(t *testing.T) {
	s := imageSchemas(t)
	unreferenced := hashed + `---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: spare}
spec: {data: {token: unused}}
`
	before, err := KindHashes(Redact(parseDocs(t, unreferenced)), s)
	if err != nil {
		t.Fatal(err)
	}
	after, err := KindHashes(Redact(parseDocs(t, strings.Replace(unreferenced, "token: unused", "token: changed", 1))), s)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("an unreferenced Secret's change: %v -> %v, want no kind to change", before, after)
	}
}

// Hashed hides a Secret's values as Hash does and keeps other documents as they are.
func TestHashed(t *testing.T) {
	docs := parseDocs(t, `apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: s}
spec: {data: {token: test}}
---
apiVersion: chalkos.dev/v1alpha1
kind: Host
metadata: {name: default}
spec: {hostname: n1}
`)
	sum := sha256.Sum256([]byte("test"))
	if got := docs[0].hidden()["spec"].(map[string]any)["data"].(map[string]any)["token"]; got != hex.EncodeToString(sum[:]) {
		t.Errorf("the Secret's token = %v", got)
	}
	if got := docs[0].Object["spec"].(map[string]any)["data"].(map[string]any)["token"]; got != "test" {
		t.Errorf("hidden changed the Secret itself: %v", got)
	}
	if !reflect.DeepEqual(docs[1].hidden(), docs[1].Object) {
		t.Errorf("the Host = %v", docs[1].hidden())
	}
}

// KindHashes of redacted documents read back as written, as a baseline reads them, are those of
// the documents Redact gave, also of a document that embeds a Secret object: reading them back
// redacts nothing again. A kind that references no Secret hashes as Hash hashes its documents,
// the embedded Secret object's values hidden alike, and a rotated value changes it.
func TestKindHashesReadBack(t *testing.T) {
	s := imageSchemas(t)
	const bundle = "---\napiVersion: bundle.example.org/v1alpha1\nkind: Bundle\nmetadata: {name: apps}\n" +
		"spec: {objects: [{apiVersion: v1, kind: Secret, metadata: {name: s}, stringData: {p: plain}}]}\n"
	docs := parseDocs(t, hashed+bundle)
	want, err := KindHashes(Redact(docs), s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := KindHashes(RedactedObjects(Redact(docs).Objects()), s)
	if err != nil || !maps.Equal(got, want) || want["Bundle"] == "" {
		t.Errorf("read back: %v, %v; redacted: %v", got, err, want)
	}
	if h, err := Hash(parseDocs(t, bundle)); err != nil || h != want["Bundle"] {
		t.Errorf("the Bundle's kind hash %s is not its Hash %s: %v", want["Bundle"], h, err)
	}
	rotated, err := KindHashes(Redact(parseDocs(t, hashed+strings.Replace(bundle, "plain", "other", 1))), s)
	if err != nil || rotated["Bundle"] == want["Bundle"] || rotated["Demo"] != want["Demo"] {
		t.Errorf("rotated: %v, %v; before: %v", rotated, err, want)
	}
}

// The values of an embedded Secret object are hashed whatever their type, so a change of a number
// in stringData, which an API server takes as a string, changes the kind hash.
func TestKindHashesSeeAnEmbeddedNumber(t *testing.T) {
	s := imageSchemas(t)
	const bundle = "apiVersion: bundle.example.org/v1alpha1\nkind: Bundle\nmetadata: {name: apps}\n" +
		"spec: {objects: [{apiVersion: v1, kind: Secret, metadata: {name: s}, stringData: {port: 1}}]}\n"
	before, err := KindHashes(Redact(parseDocs(t, bundle)), s)
	if err != nil {
		t.Fatal(err)
	}
	after, err := KindHashes(Redact(parseDocs(t, strings.Replace(bundle, "port: 1", "port: 2", 1))), s)
	if err != nil || after["Bundle"] == before["Bundle"] {
		t.Errorf("a number's change: %v -> %v, %v; want the Bundle's hash to change", before, after, err)
	}
	if text, _ := json.Marshal(Redact(parseDocs(t, bundle)).docs[0].Object); !strings.Contains(string(text), `"port":"`+sum("1")+`"`) {
		t.Errorf("redacted %s, want the number hashed", text)
	}
}

// A Secret object a document embeds is hashed by its values too, so a change of one, of any
// type, changes the config's version.
func TestHashChangesWithAnEmbeddedSecretValue(t *testing.T) {
	embedded := hashed + `---
apiVersion: demo.example.org/v1alpha1
kind: Demo
metadata: {name: c}
spec: {objects: [{apiVersion: v1, kind: Secret, metadata: {name: k}, data: {token: one}, stringData: {count: 1}}]}
`
	for _, edit := range []struct{ from, to string }{
		{"token: one", "token: two"},
		{"count: 1", "count: 2"},
	} {
		if hashOf(t, embedded) == hashOf(t, strings.Replace(embedded, edit.from, edit.to, 1)) {
			t.Errorf("%s -> %s in an embedded Secret left the hash unchanged", edit.from, edit.to)
		}
	}
}
