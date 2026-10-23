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
	want := "*[Slots]: One of the two partition pairs an image is written to.\n" +
		"*[slots]: One of the two partition pairs an image is written to.\n" +
		"*[Slot]: One of the two partition pairs an image is written to.\n" +
		"*[slot]: One of the two partition pairs an image is written to.\n" +
		"*[A/B]: The update scheme of a node's disk: two slots, A and B.\n"
	if string(got) != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
