package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

const hashed = `
apiVersion: chalkos.dev/v1alpha1
kind: Cluster
metadata: {name: lab}
spec: {endpoint: "https://10.0.0.1:6443"}
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
kind: Cluster
metadata: {name: lab}
spec: {endpoint: "https://10.0.0.1:6443"}
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
	// Numbers are hashed as written.
	if hashOf(t, strings.Replace(hashed, "name: two", "name: two, replicas: 1", 1)) == hashOf(t, strings.Replace(hashed, "name: two", "name: two, replicas: 1.0", 1)) {
		t.Error("1 and 1.0 hash alike")
	}
}

func TestHashHidesSecretValues(t *testing.T) {
	c, err := canonical(parseDocs(t, hashed))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("s3cret"))
	if bytes.Contains(c, []byte("s3cret")) || !bytes.Contains(c, []byte(hex.EncodeToString(sum[:]))) {
		t.Errorf("canonical form = %s, want the secret's SHA-256 in place of its value", c)
	}
}

func TestSecretRefs(t *testing.T) {
	docs := parseDocs(t, hashed)
	s := imageSchemas(t)
	for _, d := range docs {
		got := SecretRefs(d, s)
		var want []SecretRef
		if d.Kind == "Demo" && d.Name == "a" {
			want = []SecretRef{{Name: "demo", Key: "token"}}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: refs = %v, want %v", d, got, want)
		}
	}
	if got := SecretRefs(docs[0], nil); got != nil {
		t.Errorf("refs without schemas = %v", got)
	}
}

func TestKindHashes(t *testing.T) {
	s := imageSchemas(t)
	kindHashes := func(yaml string) map[string]string {
		t.Helper()
		h, err := KindHashes(parseDocs(t, yaml), s)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	before := kindHashes(hashed)
	if _, ok := before["Secret"]; ok || len(before) != 2 {
		t.Errorf("kinds = %v, want Cluster and Demo alone", before)
	}
	secret := kindHashes(strings.Replace(hashed, "token: s3cret", "token: other", 1))
	if secret["Demo"] == before["Demo"] || secret["Cluster"] != before["Cluster"] {
		t.Errorf("a referenced Secret's change: %v -> %v, want Demo alone to change", before, secret)
	}
	demo := kindHashes(strings.Replace(hashed, "name: two", "name: three", 1))
	if demo["Demo"] == before["Demo"] || demo["Cluster"] != before["Cluster"] {
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
	before, err := KindHashes(parseDocs(t, unreferenced), s)
	if err != nil {
		t.Fatal(err)
	}
	after, err := KindHashes(parseDocs(t, strings.Replace(unreferenced, "token: unused", "token: changed", 1)), s)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("an unreferenced Secret's change: %v -> %v, want no kind to change", before, after)
	}
}
