package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func parseDocs(t *testing.T, yaml string) []Document {
	t.Helper()
	docs, err := Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

func TestParseKeepsATimestampLikeValueVerbatim(t *testing.T) {
	docs := parseDocs(t, "apiVersion: x.example.org/v1\nkind: X\nmetadata: {name: a}\nspec: {when: 2024-01-01}\n")
	spec := docs[0].Object["spec"].(map[string]any)
	if spec["when"] != "2024-01-01" {
		t.Errorf("when = %#v, want the string \"2024-01-01\"", spec["when"])
	}
}

func TestParseKeepsABigIntegerExact(t *testing.T) {
	docs := parseDocs(t, "apiVersion: x.example.org/v1\nkind: X\nmetadata: {name: a}\nspec: {n: 123456789012345678901234567890}\n")
	spec := docs[0].Object["spec"].(map[string]any)
	if spec["n"] != json.Number("123456789012345678901234567890") {
		t.Errorf("n = %#v", spec["n"])
	}
}

func TestParseRejectsANumericKey(t *testing.T) {
	_, err := Parse(strings.NewReader("apiVersion: x.example.org/v1\nkind: X\nmetadata: {name: a}\nspec:\n  80: oops\n"))
	if err == nil || !strings.Contains(err.Error(), "document 1, line 5: key 80 is not a string; quote it") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseAcceptsAQuotedNumericKey(t *testing.T) {
	docs := parseDocs(t, "apiVersion: x.example.org/v1\nkind: X\nmetadata: {name: a}\nspec:\n  \"80\": ok\n")
	spec := docs[0].Object["spec"].(map[string]any)
	if spec["80"] != "ok" {
		t.Errorf("spec[80] = %#v", spec["80"])
	}
}

func TestParseCanonicalisesIntegers(t *testing.T) {
	docs := parseDocs(t, "apiVersion: x.example.org/v1\nkind: X\nmetadata: {name: a}\n"+
		"spec: {hex: 0x7b, oct: 0o17, sep: 1_000, neg: -0x10, big: 123456789012345678901234567890}\n")
	spec := docs[0].Object["spec"].(map[string]any)
	for key, want := range map[string]json.Number{"hex": "123", "oct": "15", "sep": "1000", "neg": "-16", "big": "123456789012345678901234567890"} {
		if spec[key] != want {
			t.Errorf("%s = %#v, want %s", key, spec[key], want)
		}
	}
}

func TestParseCanonicalisesFloats(t *testing.T) {
	docs := parseDocs(t, "apiVersion: x.example.org/v1\nkind: X\nmetadata: {name: a}\n"+
		"spec: {plain: 1.25, exact: 0.1000000000000000000001, exp: 1e3, plus: +1.5, dot: .5}\n")
	spec := docs[0].Object["spec"].(map[string]any)
	for key, want := range map[string]json.Number{"plain": "1.25", "exact": "0.1000000000000000000001", "exp": "1e3", "plus": "1.5", "dot": "0.5"} {
		if spec[key] != want {
			t.Errorf("%s = %#v, want %s", key, spec[key], want)
		}
	}
}

func TestParseRejectsNonFiniteFloats(t *testing.T) {
	_, err := Parse(strings.NewReader("apiVersion: x.example.org/v1\nkind: X\nmetadata: {name: a}\nspec:\n  n: .inf\n"))
	if err == nil || !strings.Contains(err.Error(), "document 1, line 5") {
		t.Fatalf("err = %v", err)
	}
}

func TestAHexPortTranslates(t *testing.T) {
	s := kubernetesSet(t, "{}", "---\napiVersion: time.chalkos.dev/v1alpha1\nkind: TimeSync\nmetadata: {name: t}\n"+
		"spec: {servers: [{host: ntp.example.org, port: 0x7b}]}\n")
	id, err := s.Identity(Target{Image: &testImage})
	if err != nil {
		t.Fatal(err)
	}
	if len(id.Time.Servers) != 1 || id.Time.Servers[0].Port != 123 {
		t.Errorf("servers = %+v", id.Time.Servers)
	}
}
