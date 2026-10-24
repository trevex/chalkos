package main

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// markdownFiles returns the Markdown files under dir, but not under dir/superpowers, in lexical
// order.
func markdownFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path == filepath.Join(dir, "superpowers") {
			return filepath.SkipDir
		}
		if !d.IsDir() && filepath.Ext(path) == ".md" {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// fence opens or closes a code block, also indented in a list, an admonition or a tab, and
// holds its info string. The info string of a fence of backticks has no backticks.
var fence = regexp.MustCompile("^\\s*(```+|~~~+)(.*)$")

// language returns the language of a fence's info string: its first word, or the first class of
// an attribute list ({ .sh title="x" }). A fence with attributes alone (title="x") has none.
func language(info string) string {
	info = strings.TrimSpace(info)
	if attrs, ok := strings.CutPrefix(info, "{"); ok {
		for _, a := range strings.Fields(strings.TrimSuffix(attrs, "}")) {
			if class, ok := strings.CutPrefix(a, "."); ok {
				return strings.ToLower(class)
			}
		}
		return ""
	}
	lang, _, _ := strings.Cut(info, " ")
	if strings.Contains(lang, "=") {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(lang, "."))
}

// codeBlock is a fenced code block of a Markdown file: the language and title of its info
// string, the line number of its first line, and its lines without the fence's indentation.
type codeBlock struct {
	lang   string
	title  string
	first  int
	indent int
	lines  []string
}

// titleAttr is the title attribute of a fence's info string, as in yaml title="nodes/w1.yaml".
var titleAttr = regexp.MustCompile(`title="([^"]*)"`)

// codeBlocks returns the fenced code blocks of a Markdown file whose language is one of langs.
func codeBlocks(path string, langs ...string) ([]codeBlock, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var blocks []codeBlock
	var open string // the fence of the open block
	var cur *codeBlock
	scanner := bufio.NewScanner(f)
	for n := 1; scanner.Scan(); n++ {
		line := scanner.Text()
		m := fence.FindStringSubmatch(line)
		if m != nil && m[1][0] == '`' && strings.Contains(m[2], "`") {
			m = nil
		}
		switch {
		case open == "" && m != nil:
			open = m[1]
			if lang := language(m[2]); slices.Contains(langs, lang) {
				b := codeBlock{lang: lang, first: n + 1, indent: len(line) - len(strings.TrimLeft(line, " "))}
				if t := titleAttr.FindStringSubmatch(m[2]); t != nil {
					b.title = t[1]
				}
				blocks = append(blocks, b)
				cur = &blocks[len(blocks)-1]
			}
		case open != "" && m != nil && strings.TrimSpace(m[2]) == "" && strings.HasPrefix(m[1], open):
			open, cur = "", nil
		case cur != nil:
			cur.lines = append(cur.lines, dedent(line, cur.indent))
		}
	}
	return blocks, scanner.Err()
}

// dedent removes up to n leading spaces, the indentation of a block's fence in a list, an
// admonition or a tab, from a line of the block.
func dedent(line string, n int) string {
	i := 0
	for i < n && i < len(line) && line[i] == ' ' {
		i++
	}
	return line[i:]
}
