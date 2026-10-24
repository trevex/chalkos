package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

// checkYAML validates the config documents of the YAML blocks of the Markdown files under dir,
// but not under dir/superpowers, against the schemas in schemasDir, with the parser and the
// validation chalkctl uses for a node's files. A document is checked when its group is chalkos's
// own, chalkos.dev or a subdomain of it, or one the schemas declare, so a kind that no image
// declares fails. Other documents, such as Kubernetes objects, fragments without an apiVersion
// and documents that are not objects pass unchecked. A block of snippet includes alone reads as a
// string, so it passes too: the example's own check validates the included files. A block that
// is not YAML fails. Each problem names the page's line its document starts on, or the line of a
// syntax error.
func checkYAML(dir, schemasDir string) error {
	schemas, err := schema.LoadSchemas(schemasDir)
	if err != nil {
		return err
	}
	groups := map[string]bool{}
	for _, k := range schemas.Kinds() {
		group, _, _ := strings.Cut(k.APIVersion, "/")
		groups[group] = true
	}
	files, err := markdownFiles(dir)
	if err != nil {
		return err
	}
	var problems []string
	for _, path := range files {
		blocks, err := codeBlocks(path, "yaml", "yml")
		if err != nil {
			return err
		}
		for _, b := range blocks {
			docs, line, err := blockDocuments(b.lines)
			if err != nil {
				problems = append(problems, problem(path, b.first+line-1, err))
				continue
			}
			for _, d := range docs {
				group, _, _ := strings.Cut(d.doc.APIVersion, "/")
				if group != "chalkos.dev" && !strings.HasSuffix(group, ".chalkos.dev") && !groups[group] {
					continue
				}
				if err := schemas.Validate(d.doc.Object); err != nil {
					problems = append(problems, problem(path, b.first+d.line-1, err))
				}
			}
		}
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

// yamlLine finds the line a YAML error names.
var yamlLine = regexp.MustCompile(`line ([0-9]+)`)

// blockDocuments reads the documents of a YAML block with config.Parse, each with the line of the
// block it starts on. A document that is not an object, such as a fragment of a list, is left out.
// On an error, it returns the line of the block the error is at.
func blockDocuments(lines []string) ([]blockDocument, int, error) {
	dec := yaml.NewDecoder(strings.NewReader(strings.Join(lines, "\n")))
	var docs []blockDocument
	for i := 1; ; i++ {
		var n yaml.Node
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			return docs, 0, nil
		}
		if err != nil {
			line := 1
			if m := yamlLine.FindStringSubmatch(err.Error()); m != nil {
				line, _ = strconv.Atoi(m[1])
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
		data, err := yaml.Marshal(root)
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

// writeNix writes each Nix block of the Markdown files under dir, but not under dir/superpowers,
// to outDir, named by the page's path with / as _ and the block's first line. A block whose title
// names a .nix file is a whole file, written as <page>_<line>.nix. A block without such a title is
// a fragment, written as <page>_<line>.fragment.nix: an expression, or the attributes of a module,
// which parse only within braces. A block of snippet includes alone is left out: the site puts the
// included file's lines there, and the image build that file belongs to evaluates it.
func writeNix(dir, outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	files, err := markdownFiles(dir)
	if err != nil {
		return err
	}
	for _, path := range files {
		blocks, err := codeBlocks(path, "nix")
		if err != nil {
			return err
		}
		for _, b := range blocks {
			if snippetsOnly(b.lines) {
				continue
			}
			kind := ".fragment"
			if strings.HasSuffix(b.title, ".nix") {
				kind = ""
			}
			name := fmt.Sprintf("%s_%d%s.nix", strings.ReplaceAll(filepath.ToSlash(path), "/", "_"), b.first, kind)
			if err := os.WriteFile(filepath.Join(outDir, name), []byte(strings.Join(b.lines, "\n")+"\n"), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
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
