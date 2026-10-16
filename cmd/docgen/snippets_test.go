package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Each refused document is reported at the first line of its block, and the documents the check
// passes over (a valid one, a Kubernetes object, a fragment, the plan's) are not.
func TestCheckYAML(t *testing.T) {
	err := checkYAML("testdata/snippets/docs", "testdata/snippets/schemas")
	if err == nil {
		t.Fatal("checkYAML found no refused document")
	}
	// A problem with several causes lists them on indented lines.
	var got []string
	for _, line := range strings.Split(err.Error(), "\n")[1:] {
		if !strings.HasPrefix(line, " ") {
			got = append(got, line)
		}
	}
	want := []struct{ prefix, contains string }{
		{"testdata/snippets/docs/page.md:16: ", `Node "w2"`},
		{"testdata/snippets/docs/page.md:26: ", `does not accept kind "Manifest" of apiVersion "kubernetes.chalkos.dev/v1alpha1"`},
		{"testdata/snippets/docs/page.md:47: ", `Rack "rack"`},
		{"testdata/snippets/docs/page.md:56: ", "document 1"},
	}
	if len(got) != len(want) {
		t.Fatalf("problems:\n%s\nwant %d", strings.Join(got, "\n"), len(want))
	}
	for i, w := range want {
		if !strings.HasPrefix(got[i], w.prefix) || !strings.Contains(got[i], w.contains) {
			t.Errorf("problem %d: %q, want it to start with %q and contain %q", i, got[i], w.prefix, w.contains)
		}
	}
}

// Only the block titled with a .nix file is written, under the page's path and its first line.
func TestWriteNix(t *testing.T) {
	out := t.TempDir()
	if err := writeNix("testdata/snippets/docs", out); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := "testdata_snippets_docs_page.md_62.nix"
	if strings.Join(names, " ") != want {
		t.Fatalf("written: %v, want %s", names, want)
	}
	data, err := os.ReadFile(filepath.Join(out, want))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\n  outputs = _: { };\n}\n" {
		t.Errorf("content %q", data)
	}
}

// A block in a list keeps its lines relative to its fence and knows its language and title.
func TestCodeBlocks(t *testing.T) {
	blocks, err := codeBlocks("testdata/snippets/docs/page.md", "yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 6 {
		t.Fatalf("%d yaml blocks, want 6", len(blocks))
	}
	if blocks[0].title != "nodes/w1.yaml" || blocks[0].first != 6 {
		t.Errorf("first block: title %q, first line %d", blocks[0].title, blocks[0].first)
	}
	if blocks[1].lines[0] != "apiVersion: chalkos.dev/v1alpha1" || blocks[1].lines[4] != "  imageRf: node-metal" {
		t.Errorf("indented block's lines: %q", blocks[1].lines)
	}
}
