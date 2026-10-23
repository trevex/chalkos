package main

import (
	"os"
	"testing"
)

func TestAbbreviations(t *testing.T) {
	src, err := os.ReadFile("testdata/glossary.md")
	if err != nil {
		t.Fatal(err)
	}
	got, err := abbreviations(src)
	if err != nil {
		t.Fatal(err)
	}
	// Effect has no lower-case form, so "take effect" gets no tooltip, and Secure Boot gets no
	// "secure Boot".
	want := "*[Secure Boot]: The firmware's check of a boot binary, i.e. its signature.\n" +
		"*[Effects]: What a delivery does, e.g. a restart.\n" +
		"*[effects]: What a delivery does, e.g. a restart.\n" +
		"*[Effect]: What a delivery does, e.g. a restart.\n" +
		"*[Slots]: One of the two partition pairs an image is written to.\n" +
		"*[slots]: One of the two partition pairs an image is written to.\n" +
		"*[Slot]: One of the two partition pairs an image is written to.\n" +
		"*[slot]: One of the two partition pairs an image is written to.\n" +
		"*[A/B]: The update scheme of a node's disk: two slots, A and B.\n"
	if string(got) != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestLowerForm(t *testing.T) {
	for term, want := range map[string]string{
		"Boot counting":    "boot counting",
		"Secure Boot":      "",
		"Kubernetes share": "kubernetes share",
		"UKI":              "",
		"db and dbx":       "",
		"Node CA":          "node CA",
	} {
		if got := lowerForm(term); got != want {
			t.Errorf("lowerForm(%q) = %q, want %q", term, got, want)
		}
	}
}

func TestFirstSentence(t *testing.T) {
	for paragraph, want := range map[string]string{
		"One. Two.":                          "One.",
		"A set, e.g. two of them. Then more": "A set, e.g. two of them.",
		"A copy, i.e. the same bytes. More":  "A copy, i.e. the same bytes.",
		"A [link](x.md) and `code`. More":    "A link and code.",
		"No full stop":                       "No full stop",
		"Ends in e.g. ":                      "Ends in e.g.",
	} {
		if got := firstSentence(paragraph); got != want {
			t.Errorf("firstSentence(%q) = %q, want %q", paragraph, got, want)
		}
	}
}
