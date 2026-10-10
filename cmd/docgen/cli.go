package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

// The lines of zensical.toml between which docgen writes the command pages' navigation.
const (
	navBegin = "# BEGIN docgen cli"
	navEnd   = "# END docgen cli"
)

// navPrefix is where the pages are in the site, relative to its docs directory.
const navPrefix = "reference/cli/"

// writeCLI writes a page per command of the programs, an index of the programs, and the pages'
// navigation into the Zensical configuration. Pages of commands that no longer exist are removed.
func writeCLI(roots []*cobra.Command, dir, config string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	index := frontMatter("Command line", "The commands of chalkctl and chalklab") +
		"# Command line\n\nchalkos has two command line tools. Their pages are generated from their help.\n\n"
	nav := []string{fmt.Sprintf("{ %q = [", "Command line"), fmt.Sprintf("  %q,", navPrefix+"index.md")}
	for _, root := range roots {
		index += fmt.Sprintf("- [%s](%s): %s\n", root.Name(), pageName(root), root.Short)
		if err := writePages(root, dir); err != nil {
			return err
		}
		for _, line := range navEntry(root) {
			nav = append(nav, "  "+line)
		}
	}
	index += "\nBoth print the same text with `--help`, and `completion` writes a shell completion script.\n"
	nav = append(nav, "] },")
	if err := os.WriteFile(filepath.Join(dir, "index.md"), []byte(index), 0o644); err != nil {
		return err
	}
	return spliceNav(config, nav)
}

// writePages writes the page of cmd and of each command below it.
func writePages(cmd *cobra.Command, dir string) error {
	cmd.DisableAutoGenTag = true
	// chalkctl and chalklab run a command that holds subcommands only to fail with its usage, so
	// its page lists the subcommands without a usage line of its own.
	if cmd.HasAvailableSubCommands() {
		cmd.Run, cmd.RunE = nil, nil
	}
	var buf bytes.Buffer
	if err := doc.GenMarkdownCustom(cmd, &buf, func(s string) string { return s }); err != nil {
		return err
	}
	page := frontMatter(cmd.CommandPath(), cmd.Short) + markdown(buf.String())
	if err := os.WriteFile(filepath.Join(dir, pageName(cmd)), []byte(page), 0o644); err != nil {
		return err
	}
	for _, c := range documented(cmd) {
		if err := writePages(c, dir); err != nil {
			return err
		}
	}
	return nil
}

// documented are the commands below cmd that get pages, as cobra's doc package chooses them.
func documented(cmd *cobra.Command) []*cobra.Command {
	var cmds []*cobra.Command
	for _, c := range cmd.Commands() {
		if c.IsAvailableCommand() && !c.IsAdditionalHelpTopicCommand() {
			cmds = append(cmds, c)
		}
	}
	return cmds
}

// pageName is the file of a command's page, as cobra's doc package links it.
func pageName(cmd *cobra.Command) string {
	return strings.ReplaceAll(cmd.CommandPath(), " ", "_") + ".md"
}

// navEntry is the navigation of cmd's page and those below it: a page of its own, or a section
// whose first page is the command's own.
func navEntry(cmd *cobra.Command) []string {
	page := navPrefix + pageName(cmd)
	children := documented(cmd)
	if len(children) == 0 {
		return []string{fmt.Sprintf("{ %q = %q },", cmd.Name(), page)}
	}
	lines := []string{fmt.Sprintf("{ %q = [", cmd.Name()), fmt.Sprintf("  { %q = %q },", "Overview", page)}
	for _, c := range children {
		for _, line := range navEntry(c) {
			lines = append(lines, "  "+line)
		}
	}
	return append(lines, "] },")
}

// spliceNav replaces the lines between the markers in the Zensical configuration with nav,
// indented as the begin marker.
func spliceNav(config string, nav []string) error {
	data, err := os.ReadFile(config)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	begin, end := -1, -1
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case navBegin:
			begin = i
		case navEnd:
			end = i
		}
	}
	if begin < 0 || end < begin {
		return fmt.Errorf("%s has no lines %q and %q to write the commands' navigation between", config, navBegin, navEnd)
	}
	indent := lines[begin][:len(lines[begin])-len(strings.TrimLeft(lines[begin], " "))]
	out := append([]string{}, lines[:begin+1]...)
	for _, line := range nav {
		out = append(out, indent+line)
	}
	out = append(out, lines[end:]...)
	return os.WriteFile(config, []byte(strings.Join(out, "\n")), 0o644)
}

// frontMatter is a page's Zensical front matter. JSON strings are YAML strings.
func frontMatter(title, description string) string {
	t, _ := json.Marshal(title)
	d, _ := json.Marshal(description)
	return fmt.Sprintf("---\ntitle: %s\ndescription: %s\n---\n\n", t, d)
}

// tag is what Markdown would take for an HTML tag in the help's prose, as <node> or <file>.prev.
var tag = regexp.MustCompile(`<([A-Za-z/])`)

// markdown makes a page of cobra's doc package fit the site: its headings start at the first
// level, as the page's title, and placeholders such as <node> in prose are escaped, so they
// show instead of being taken for HTML. Code blocks stay as they are.
func markdown(page string) string {
	lines := strings.Split(page, "\n")
	fenced := false
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "```"):
			fenced = !fenced
		case fenced, strings.HasPrefix(line, "\t"), strings.HasPrefix(line, "    "):
		case strings.HasPrefix(line, "## "), strings.HasPrefix(line, "### "):
			lines[i] = tag.ReplaceAllString(line[1:], "&lt;$1")
		default:
			lines[i] = tag.ReplaceAllString(line, "&lt;$1")
		}
	}
	return strings.Join(lines, "\n")
}
