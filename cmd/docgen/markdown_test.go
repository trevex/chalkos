package main

import (
	"strings"
	"testing"
)

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

// The Markdown files of a directory are those below it, in lexical order, but not those under its
// superpowers directory; a file names itself.
func TestMarkdownFiles(t *testing.T) {
	for dir, want := range map[string]string{
		"testdata/docs":          "testdata/docs/guides/bad.md testdata/docs/index.md",
		"testdata/docs/index.md": "testdata/docs/index.md",
	} {
		files, err := markdownFiles(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(files, " "); got != want {
			t.Errorf("markdownFiles(%s) = %s, want %s", dir, got, want)
		}
	}
}
