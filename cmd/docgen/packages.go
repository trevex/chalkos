package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// checkPackages fails when a Go directory below root's cmd/ or pkg/ has no row in the tables of
// the page at path. A Go directory holds a .go file other than a test, outside testdata; a
// directory without one, such as pkg/api, or one of data alone, needs no row. A row names its
// directories in code spans in its first cell: cmd/<program> for a program, and a path relative to
// pkg/ for a package. Each directory needs a row of its own, as a parent package's row does not
// describe its subpackages.
func checkPackages(path, root string) error {
	page, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	named := map[string]bool{}
	for _, line := range strings.Split(string(page), "\n") {
		cells := strings.Split(line, "|")
		if !strings.HasPrefix(line, "|") || len(cells) < 3 {
			continue
		}
		for _, m := range codeSpan.FindAllStringSubmatch(cells[1], -1) {
			named[strings.TrimSuffix(m[1], "/")] = true
		}
	}
	var missing []string
	for _, top := range []string{"cmd", "pkg"} {
		dirs, err := goDirs(filepath.Join(root, top))
		if err != nil {
			return err
		}
		for _, dir := range dirs {
			name := dir
			if top == "cmd" {
				name = "cmd/" + dir
			}
			if !named[name] {
				missing = append(missing, top+"/"+dir)
			}
		}
	}
	if len(missing) > 0 {
		return errors.New(path + " has no row for these directories of Go code; add one to the table of their area:\n" + strings.Join(missing, "\n"))
	}
	return nil
}

// codeSpan matches a code span of Markdown.
var codeSpan = regexp.MustCompile("`([^`]+)`")

// goDirs returns the directories below dir, relative to it and sorted, that hold a .go file other
// than a test, leaving out testdata.
func goDirs(dir string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "testdata" {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(dir, filepath.Dir(path))
		if err != nil {
			return err
		}
		if rel = filepath.ToSlash(rel); !slices.Contains(dirs, rel) {
			dirs = append(dirs, rel)
		}
		return nil
	})
	slices.Sort(dirs)
	return dirs, err
}
