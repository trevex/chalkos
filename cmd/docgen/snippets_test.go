package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Each refused document is reported at its first line, and a syntax error at its line; the
// documents the check passes over (a valid one, a Kubernetes object, a fragment, a list, the
// plan's) are not.
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
		{"testdata/snippets/docs/page.md:86: ", `Node "w5"`},
		{"testdata/snippets/docs/page.md:94: ", "document 1"},
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

// A block titled with a .nix file is written as a whole file and the other as a fragment, under
// the page's path and its first line; the plan's, and a block that only includes a section of a
// file, are not written.
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
	if got := strings.Join(names, " "); got != want+" testdata_snippets_docs_page.md_68.fragment.nix" {
		t.Fatalf("written: %s", got)
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
	if len(blocks) != 11 {
		t.Fatalf("%d yaml blocks, want 11", len(blocks))
	}
	if blocks[0].title != "nodes/w1.yaml" || blocks[0].first != 6 {
		t.Errorf("first block: title %q, first line %d", blocks[0].title, blocks[0].first)
	}
	if blocks[1].lines[0] != "apiVersion: chalkos.dev/v1alpha1" || blocks[1].lines[4] != "  hostnme: node-metal" {
		t.Errorf("indented block's lines: %q", blocks[1].lines)
	}
}
