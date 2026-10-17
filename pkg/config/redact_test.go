package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

const withSecrets = `
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: demo}
spec: {data: {token: s3cret}}
---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Addon
metadata: {name: apps}
spec:
  objects:
    - {apiVersion: v1, kind: Secret, metadata: {name: s}, data: {k: dg==}, stringData: {p: plain}}
    - {apiVersion: v1, kind: ConfigMap, metadata: {name: c}, data: {k: visible}}
    - {apiVersion: v1, kind: List, items: [{apiVersion: v1, kind: Secret, metadata: {name: l}, data: {k: bGlzdA==}}, {apiVersion: v1, kind: ConfigMap, metadata: {name: lc}, data: {k: shown}}]}
    - {apiVersion: v1, kind: secret, metadata: {name: lower}, stringData: {p: hidden}}
    - {apiVersion: v1, kind: SecretList, items: [{apiVersion: v1, kind: SECRET, metadata: {name: up}, data: {k: deep}}]}
---
apiVersion: chalkos.dev/v1alpha1
kind: Node
metadata: {name: n1}
spec: {imageRef: metal}
`

func sum(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
}

// TestRedact replaces the values of Secrets and of the Kubernetes Secrets an Addon applies, also in
// a list's items and of a kind in other casing, by their
// SHA-256, as Hash hashes a Secret's, keeps everything else and leaves its input as it was.
func TestRedact(t *testing.T) {
	docs := parseDocs(t, withSecrets)
	before, err := json.Marshal(docs[1].Object)
	if err != nil {
		t.Fatal(err)
	}
	got := Redact(docs)
	text, err := json.Marshal([]any{got[0].Object, got[1].Object, got[2].Object})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"token":"` + sum("s3cret") + `"`, `"k":"` + sum("dg==") + `"`, `"p":"` + sum("plain") + `"`, `"k":"visible"`, `"k":"shown"`, `"k":"` + sum("bGlzdA==") + `"`, `"p":"` + sum("hidden") + `"`, `"k":"` + sum("deep") + `"`, `"imageRef":"metal"`} {
		if !strings.Contains(string(text), want) {
			t.Errorf("redacted %s, want it to hold %s", text, want)
		}
	}
	for _, secret := range []string{"s3cret", "dg==", "plain", "bGlzdA==", "hidden", "deep"} {
		if strings.Contains(string(text), `"`+secret+`"`) {
			t.Errorf("redacted %s holds %s", text, secret)
		}
	}
	if after, _ := json.Marshal(docs[1].Object); string(after) != string(before) {
		t.Errorf("the input changed: %s", after)
	}
	// The same documents redact the same, so two redacted sets compare as the sets do.
	if a, b := mustHash(t, Redact(docs)), mustHash(t, got); a != b {
		t.Error("redacting twice differs")
	}
}

// TestRedacted tells the fields whose values Redact replaces.
func TestRedacted(t *testing.T) {
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
		{2, "spec.imageRef", false},
	} {
		if got := Redacted(docs[tc.doc], strings.Split(tc.path, ".")); got != tc.want {
			t.Errorf("Redacted(%s, %s) = %v, want %v", docs[tc.doc], tc.path, got, tc.want)
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
