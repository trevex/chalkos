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
