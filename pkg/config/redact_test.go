package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const withSecrets = `
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: demo}
spec: {data: {token: s3cret}}
---
apiVersion: bundle.example.org/v1alpha1
kind: Bundle
metadata: {name: apps}
spec:
  objects:
    - {apiVersion: v1, kind: Secret, metadata: {name: s}, data: {k: dg==}, stringData: {p: plain}}
    - {apiVersion: v1, kind: ConfigMap, metadata: {name: c}, data: {k: visible}}
    - {apiVersion: v1, kind: List, items: [{apiVersion: v1, kind: Secret, metadata: {name: l}, data: {k: bGlzdA==}}, {apiVersion: v1, kind: ConfigMap, metadata: {name: lc}, data: {k: shown}}]}
    - {apiVersion: v1, kind: secret, metadata: {name: lower}, stringData: {p: hidden}}
    - {apiVersion: v1, kind: SecretList, items: [{apiVersion: v1, kind: SECRET, metadata: {name: up}, data: {k: deep}}]}
    - {apiVersion: /v1, kind: Secret, metadata: {name: slash}, data: {k: c2xhc2g=}}
---
apiVersion: chalkos.dev/v1alpha1
kind: Host
metadata: {name: default}
spec: {hostname: h1}
`

func sum(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
}

// TestRedact replaces the values of Secrets and of the Secret objects a document embeds, also in a
// list's items, of a kind in other casing and of apiVersion /v1, by their SHA-256, as Hash hashes a
// Secret's, keeps everything else and leaves its input as it was.
func TestRedact(t *testing.T) {
	docs := parseDocs(t, withSecrets)
	before, err := json.Marshal(docs[1].Object)
	if err != nil {
		t.Fatal(err)
	}
	got := Redact(docs).docs
	text, err := json.Marshal([]any{got[0].Object, got[1].Object, got[2].Object})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"token":"` + sum("s3cret") + `"`, `"k":"` + sum("dg==") + `"`, `"p":"` + sum("plain") + `"`, `"k":"visible"`, `"k":"shown"`, `"k":"` + sum("bGlzdA==") + `"`, `"p":"` + sum("hidden") + `"`, `"k":"` + sum("deep") + `"`, `"k":"` + sum("c2xhc2g=") + `"`, `"hostname":"h1"`} {
		if !strings.Contains(string(text), want) {
			t.Errorf("redacted %s, want it to hold %s", text, want)
		}
	}
	for _, secret := range []string{"s3cret", "dg==", "plain", "bGlzdA==", "hidden", "deep", "c2xhc2g="} {
		if strings.Contains(string(text), `"`+secret+`"`) {
			t.Errorf("redacted %s holds %s", text, secret)
		}
	}
	if after, _ := json.Marshal(docs[1].Object); string(after) != string(before) {
		t.Errorf("the input changed: %s", after)
	}
	// The same documents redact the same, so two redacted sets compare as the sets do.
	if !reflect.DeepEqual(Redact(docs).docs, got) {
		t.Error("redacting the same documents again differs")
	}
}

// TestSecretField tells the fields whose values Redact replaces.
func TestSecretField(t *testing.T) {
	docs := parseDocs(t, withSecrets)
	for _, tc := range []struct {
		doc  int
		path string
		want bool
	}{
		{0, "spec.data.token", true},
		{0, "spec.data", true},
		{0, "metadata.name", false},
		{1, "spec.objects.0.data.k", true},
		{1, "spec.objects.0.stringData.p", true},
		{1, "spec.objects.0.metadata.name", false},
		{1, "spec.objects.1.data.k", false},
		{1, "spec.objects.2.items.0.data.k", true},
		{1, "spec.objects.2.items.1.data.k", false},
		{1, "spec.objects.2.items", false},
		{1, "spec.objects.3.stringData.p", true},
		{1, "spec.objects.4.items.0.data.k", true},
		{1, "spec.objects.4.items.0.metadata.name", false},
		{1, "spec.objects.5.data.k", true},
		{2, "spec.hostname", false},
	} {
		if got := SecretField(docs[tc.doc], strings.Split(tc.path, ".")); got != tc.want {
			t.Errorf("SecretField(%s, %s) = %v, want %v", docs[tc.doc], tc.path, got, tc.want)
		}
	}
}

func mustHash(t *testing.T, docs []Document) string {
	t.Helper()
	h, err := Hash(docs)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// Redacted holds documents with their values hidden once: it converts to no slice of documents,
// so Redact cannot take it, and has no field another package can set, so only Redact and the
// readers of redacted documents produce it.
func TestRedactedIsNoSliceOfDocuments(t *testing.T) {
	r := reflect.TypeFor[Redacted]()
	for _, raw := range []reflect.Type{reflect.TypeFor[[]Document](), reflect.TypeFor[Documents]()} {
		if r.ConvertibleTo(raw) || r.AssignableTo(raw) || raw.ConvertibleTo(r) {
			t.Errorf("Redacted converts to or from %s", raw)
		}
	}
	for f := range r.Fields() {
		if f.IsExported() {
			t.Errorf("Redacted has the exported field %s", f.Name)
		}
	}
}

// Redacted documents read back as they were written, by RedactedObjects or ParseRedacted, are
// those Redact gave: reading them hides nothing again, so their hashes and their Secret values
// stay those of the documents with their values.
func TestRedactedReadBack(t *testing.T) {
	docs := parseDocs(t, withSecrets)
	want := Redact(docs)
	text, err := Encode(want.Documents())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseRedacted(strings.NewReader(text.Text))
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]Redacted{"RedactedObjects": RedactedObjects(want.Objects()), "ParseRedacted": parsed} {
		if !reflect.DeepEqual(got.Objects(), want.Objects()) {
			t.Errorf("%s: %v, want %v", name, got.Objects(), want.Objects())
		}
		h, err := got.hash()
		if err != nil || h != mustHash(t, docs) {
			t.Errorf("%s: hash %s, %v; want Hash of the documents %s", name, h, err, mustHash(t, docs))
		}
	}
}

// Hash is the hash of the documents Redact gives, so it hides every value Redact hides, an
// embedded Secret object's among them.
func TestHashIsOfRedact(t *testing.T) {
	docs := parseDocs(t, withSecrets)
	rotated := parseDocs(t, strings.Replace(withSecrets, "stringData: {p: plain}", "stringData: {p: other}", 1))
	if want, err := Redact(docs).hash(); err != nil || mustHash(t, docs) != want {
		t.Errorf("Hash %s, want %s: %v", mustHash(t, docs), want, err)
	}
	if mustHash(t, docs) == mustHash(t, rotated) {
		t.Error("an embedded Secret object's rotated value hashes alike")
	}
}

// CheckRedacted accepts a set without a SystemDisk, fills in its defaults and keeps it redacted,
// and refuses what Translate refuses of a set: an unknown kind, a reference that does not
// resolve, and a Mirror that RegistriesOf refuses.
func TestCheckRedacted(t *testing.T) {
	got, err := CheckRedacted(Redact(parseDocs(t, registriesDocs)), schemas(t))
	if err != nil {
		t.Fatalf("CheckRedacted = %v", err)
	}
	text, err := json.Marshal(got.Objects())
	if err != nil {
		t.Fatal(err)
	}
	if got.Len() != 4 || !strings.Contains(string(text), `"token":"`+sum("s3cret")+`"`) || strings.Contains(string(text), `"s3cret"`) {
		t.Errorf("CheckRedacted = %s, want the four documents with the Secret's value hidden", text)
	}
	if want, _, err := TranslateRedacted("n1", Redact(parseDocs(t, minimal+"---\n"+registriesDocs)), schemas(t), Target{Platform: "metal"}); err != nil || !reflect.DeepEqual(got.Objects(), slices.DeleteFunc(want.Objects(), func(o map[string]any) bool { return o["kind"] == "SystemDisk" })) {
		t.Errorf("CheckRedacted's documents differ from TranslateRedacted's: %v", err)
	}
	for name, tc := range map[string]struct{ docs, want string }{
		"an unknown kind":                   {strings.Replace(registriesDocs, "kind: Mirror", "kind: Mirorr", 1), `does not accept kind "Mirorr"`},
		"a reference that does not resolve": {strings.Replace(registriesDocs, "name: hub, key: token", "name: lab, key: token", 1), `"lab"`},
		"a plain HTTP endpoint":             {strings.Replace(registriesDocs, "insecure: true", "insecure: false", 1), "is plain HTTP"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CheckRedacted(Redact(parseDocs(t, tc.docs)), schemas(t)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
