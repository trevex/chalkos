package main

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

// checkYAML validates the config documents of the docs' YAML blocks against the schemas in
// schemasDir, as chalkctl parses and validates a node's files. Only chalkos groups (chalkos.dev and
// its subdomains) and groups the schemas declare are checked, so an undeclared chalkos kind fails.
// Section includes are expanded first, as the site renders them; a block of whole-file includes
// reads as a string and passes. A block that is not YAML fails. Problems name the page line of the
// document or the syntax error; a section's lines are at their include line.
func checkYAML(dir, schemasDir string) error {
	schemas, err := schema.LoadSchemas(schemasDir)
	if err != nil {
		return err
	}
	groups := map[string]bool{}
	for _, k := range schemas.Kinds() {
		groups[k.Group()] = true
	}
	var problems []string
	err = eachBlock(dir, []string{"yaml", "yml"}, func(path string, b codeBlock) error {
		lines, source, err := expandSections(b.lines)
		if err != nil {
			problems = append(problems, problem(path, b.first, err))
			return nil
		}
		docs, line, err := blockDocuments(lines)
		if err != nil {
			problems = append(problems, problem(path, b.first+sourceLine(source, line)-1, err))
			return nil
		}
		for _, d := range docs {
			group, _, _ := strings.Cut(d.doc.APIVersion, "/")
			if group != "chalkos.dev" && !strings.HasSuffix(group, ".chalkos.dev") && !groups[group] {
				continue
			}
			if err := schemas.Validate(d.doc.Object); err != nil {
				problems = append(problems, problem(path, b.first+sourceLine(source, d.line)-1, err))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return errors.New("config documents in the documentation that the schemas refuse:\n" + strings.Join(problems, "\n"))
	}
	return nil
}

// blockDocument is a config document of a YAML block and the line of the block it starts on.
type blockDocument struct {
	line int
	doc  config.Document
}

// blockDocuments reads the documents of a YAML block with config.Parse, each with the line of the
// block it starts on. A document that is not an object, such as a fragment of a list, is left out.
// On an error, it returns the line of the block the error is at.
func blockDocuments(lines []string) ([]blockDocument, int, error) {
	loader, err := yaml.NewLoader(strings.NewReader(strings.Join(lines, "\n")))
	if err != nil {
		return nil, 1, err
	}
	var docs []blockDocument
	for i := 1; ; i++ {
		var n yaml.Node
		err := loader.Load(&n)
		if errors.Is(err, io.EOF) {
			return docs, 0, nil
		}
		if err != nil {
			line := 1
			if loadErr, ok := errors.AsType[*yaml.LoadError](err); ok {
				// The start of the construct the error is in, such as a flow sequence's [, or
				// else the error's own position, which at the end of the block is a line past it.
				line = max(1, min(cmp.Or(loadErr.ContextMark.Line, loadErr.Mark.Line), len(lines)))
			}
			return nil, line, fmt.Errorf("document %d: %w", i, err)
		}
		root := &n
		if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
			root = n.Content[0]
		}
		if root.Kind != yaml.MappingNode {
			continue
		}
		// config.Parse reads the document as chalkctl does, from its own text.
		data, err := yaml.Dump(root)
		if err == nil {
			var parsed []config.Document
			if parsed, err = config.Parse(bytes.NewReader(data)); err == nil {
				for _, d := range parsed {
					docs = append(docs, blockDocument{line: root.Line, doc: d})
				}
				continue
			}
		}
		return nil, root.Line, fmt.Errorf("document %d: %w", i, err)
	}
}

// writeNix writes each Nix block of the docs under dir to outDir, named by the page's path with /
// as _ and the block's first line: <page>_<line>.nix when its title names a .nix file, else
// <page>_<line>.fragment.nix, an expression or a module's attributes. A block of snippet includes
// alone is skipped: the image build it belongs to evaluates it.
func writeNix(dir, outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	return eachBlock(dir, []string{"nix"}, func(path string, b codeBlock) error {
		if snippetsOnly(b.lines) {
			return nil
		}
		kind := ".fragment"
		if strings.HasSuffix(b.title, ".nix") {
			kind = ""
		}
		name := fmt.Sprintf("%s_%d%s.nix", strings.ReplaceAll(filepath.ToSlash(path), "/", "_"), b.first, kind)
		if err := os.WriteFile(filepath.Join(outDir, name), []byte(strings.Join(b.lines, "\n")+"\n"), 0o644); err != nil {
			return err
		}
		return nil
	})
}

// snippetInclude matches a line of pymdownx.snippets that includes a file or a section of one.
var snippetInclude = regexp.MustCompile(`^\s*--8<--\s`)

// snippetsOnly tells whether a block's lines, but for blank ones, are all snippet includes.
func snippetsOnly(lines []string) bool {
	found := false
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if !snippetInclude.MatchString(l) {
			return false
		}
		found = true
	}
	return found
}

// problem reports err at a block's first line; the lines of an error that lists several causes
// are indented under it.
func problem(path string, line int, err error) string {
	return fmt.Sprintf("%s:%d: %s", path, line, strings.ReplaceAll(err.Error(), "\n", "\n    "))
}

// snippetSection matches a line that includes a section of a file, as pymdownx.snippets reads it:
// its indentation, the file, relative to the site's root, and the section's name.
var snippetSection = regexp.MustCompile(`^([ \t]*)--8<--\s+"([^":]+):([A-Za-z0-9_-]+)"\s*$`)

// expandSections replaces each section include with the section's lines, dedented and then indented
// as the include, as zensical.toml renders them; paths are relative to the site's root, docgen's
// working directory. It returns each line's source line in lines, from 1; a section's lines come
// from their include line.
func expandSections(lines []string) (out []string, source []int, err error) {
	for i, l := range lines {
		m := snippetSection.FindStringSubmatch(l)
		if m == nil {
			out, source = append(out, l), append(source, i+1)
			continue
		}
		section, err := fileSection(m[2], m[3])
		if err != nil {
			return nil, nil, err
		}
		for _, s := range section {
			if strings.TrimSpace(s) != "" {
				s = m[1] + s
			}
			out, source = append(out, s), append(source, i+1)
		}
	}
	return out, source, nil
}

// sourceLine is the line of a block that line n of its expanded lines comes from, by the source
// lines expandSections returns. A line past the end, as an error at the end of the text names,
// counts on from the block's last line.
func sourceLine(source []int, n int) int {
	if n < 1 {
		return 1
	}
	if n <= len(source) {
		return source[n-1]
	}
	last := 0
	if len(source) > 0 {
		last = source[len(source)-1]
	}
	return last + n - len(source)
}

// fileSection reads the lines between the markers "--8<-- [start:<name>]" and "--8<-- [end:<name>]"
// of the file, without their common indentation.
func fileSection(path, name string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var section []string
	in, found := false, false
	for _, l := range strings.Split(string(data), "\n") {
		switch {
		case strings.Contains(l, "--8<-- [start:"+name+"]"):
			in, found = true, true
		case strings.Contains(l, "--8<-- [end:"+name+"]"):
			in = false
		case in:
			section = append(section, l)
		}
	}
	if !found {
		return nil, fmt.Errorf("%s has no section %s", path, name)
	}
	indent := -1
	for _, l := range section {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if n := len(l) - len(strings.TrimLeft(l, " \t")); indent < 0 || n < indent {
			indent = n
		}
	}
	for i, l := range section {
		if strings.TrimSpace(l) == "" {
			section[i] = ""
		} else {
			section[i] = l[indent:]
		}
	}
	return section, nil
}
