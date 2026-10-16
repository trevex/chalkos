package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
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

// checkYAML validates the config documents of the YAML blocks of the Markdown files under dir,
// but not under dir/superpowers, against the schemas in schemasDir, with the parser and the
// validation chalkctl uses for a node's files. A document is checked when its group is chalkos's
// own, chalkos.dev or a subdomain of it, or one the schemas declare, so a kind that no image
// declares fails. Other documents, such as Kubernetes objects, and fragments without an
// apiVersion pass unchecked. A block that is not YAML fails.
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
			docs, err := config.Parse(strings.NewReader(strings.Join(b.lines, "\n")))
			if err != nil {
				problems = append(problems, problem(path, b.first, err))
				continue
			}
			for _, d := range docs {
				group, _, _ := strings.Cut(d.APIVersion, "/")
				if group != "chalkos.dev" && !strings.HasSuffix(group, ".chalkos.dev") && !groups[group] {
					continue
				}
				if err := schemas.Validate(d.Object); err != nil {
					problems = append(problems, problem(path, b.first, err))
				}
			}
		}
	}
	if len(problems) > 0 {
		return errors.New("config documents in the documentation that the schemas refuse:\n" + strings.Join(problems, "\n"))
	}
	return nil
}

// writeNix writes each Nix block of the Markdown files under dir, but not under dir/superpowers,
// whose title names a .nix file, so the block is a whole file, to outDir as <page>_<line>.nix:
// the page's path with / as _, and the block's first line. A fragment of a file has no title and
// is not written, since it does not parse alone.
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
			if !strings.HasSuffix(b.title, ".nix") {
				continue
			}
			name := fmt.Sprintf("%s_%d.nix", strings.ReplaceAll(filepath.ToSlash(path), "/", "_"), b.first)
			if err := os.WriteFile(filepath.Join(outDir, name), []byte(strings.Join(b.lines, "\n")+"\n"), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// problem reports err at a block's first line; the lines of an error that lists several causes
// are indented under it.
func problem(path string, line int, err error) string {
	return fmt.Sprintf("%s:%d: %s", path, line, strings.ReplaceAll(err.Error(), "\n", "\n    "))
}
