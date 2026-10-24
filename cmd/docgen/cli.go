package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
// docs is the site's docs directory, whose pages title the links of help to the site.
func writeCLI(roots []*cobra.Command, dir, config, docs string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	index := frontMatter("Command line", "The commands of chalkctl, chalklab, chalkbuild and chalkcfg") +
		"# Command line\n\nchalkos has four command line tools: chalkctl, chalklab and chalkbuild run on an operator's machine, chalkcfg on a node. Their pages are generated from their help.\n\n"
	nav := []string{fmt.Sprintf("{ %q = [", "Command line"), fmt.Sprintf("  %q,", navPrefix+"index.md")}
	for _, root := range roots {
		index += fmt.Sprintf("- [%s](%s): %s\n", root.Name(), pageName(root), root.Short)
		if err := writePages(root, dir, docs); err != nil {
			return err
		}
		for _, line := range navEntry(root) {
			nav = append(nav, "  "+line)
		}
	}
	index += "\nEvery command shows its description, examples and flags with `--help`. chalkctl's, chalklab's and chalkbuild's `completion` writes a shell completion script.\n"
	nav = append(nav, "] },")
	if err := os.WriteFile(filepath.Join(dir, "index.md"), []byte(index), 0o644); err != nil {
		return err
	}
	return spliceNav(config, nav)
}

// writePages writes the page of cmd and of each command below it.
func writePages(cmd *cobra.Command, dir, docs string) error {
	body, err := render(cmd, docs)
	if err != nil {
		return fmt.Errorf("%s: %w", cmd.CommandPath(), err)
	}
	page := frontMatter(cmd.CommandPath(), cmd.Short) + body
	if err := os.WriteFile(filepath.Join(dir, pageName(cmd)), []byte(page), 0o644); err != nil {
		return err
	}
	for _, c := range documented(cmd) {
		if err := writePages(c, dir, docs); err != nil {
			return err
		}
	}
	return nil
}

// render is the body of a command's page: its usage line, its description, its flags, its
// examples and the pages of the commands above and below it. A command that holds subcommands
// gets no usage line: chalkctl, chalklab, chalkbuild and chalkcfg run one only to fail with its
// usage. The completion command's page shows each shell's usage line and how to load its script.
func render(cmd *cobra.Command, docs string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", cmd.CommandPath())
	var usage []string
	shells := collapsed(cmd)
	switch {
	case shells:
		// cobra leaves [flags] out of bash's usage line alone, though it has the same flags.
		for _, c := range available(cmd) {
			line := c.CommandPath()
			if c.HasAvailableLocalFlags() {
				line += " [flags]"
			}
			usage = append(usage, line)
		}
	case !cmd.HasAvailableSubCommands():
		usage = []string{cmd.UseLine()}
	}
	if len(usage) > 0 {
		fmt.Fprintf(&b, "```sh\n%s\n```\n\n", strings.Join(usage, "\n"))
	}
	if cmd.Long != "" {
		long, err := siteLinks(prose(strings.TrimSpace(cmd.Long)), docs)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s\n\n", long)
	}
	flags := cmd.LocalFlags()
	if shells && len(available(cmd)) > 0 {
		flags = available(cmd)[0].LocalFlags()
	}
	b.WriteString(flagTable("Flags", flags))
	b.WriteString(flagTable("Inherited flags", cmd.InheritedFlags()))
	if shells {
		b.WriteString("## Shells\n\n| Shell | Current session | Every new session |\n| --- | --- | --- |\n")
		for _, c := range available(cmd) {
			current, every := "—", "—"
			if load, ok := completionLoad[c.Name()]; ok {
				current = tableCell(code(fmt.Sprintf(load.current, cmd.Root().Name())))
				every = tableCell(fmt.Sprintf(load.every, cmd.Root().Name()))
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", c.Name(), current, every)
		}
		b.WriteString("\n")
	}
	if cmd.Example != "" {
		fmt.Fprintf(&b, "## Examples\n\n```sh\n%s\n```\n\n", examples(cmd.Example))
	}
	var related []*cobra.Command
	if cmd.HasParent() {
		related = append(related, cmd.Parent())
	}
	related = append(related, documented(cmd)...)
	if len(related) > 0 {
		b.WriteString("## See also\n\n")
		for _, c := range related {
			fmt.Fprintf(&b, "- [%s](%s): %s\n", c.CommandPath(), pageName(c), prose(c.Short))
		}
	}
	return strings.TrimSuffix(b.String(), "\n") + "\n", nil
}

// completionLoad is how a shell loads its completion script, by the program's name: the command
// that loads it into the current session, and the setup that loads it into every new one,
// after cobra's help of each shell's completion command.
var completionLoad = map[string]struct{ current, every string }{
	"bash": {"source <(%[1]s completion bash)",
		"With the bash-completion package installed, `%[1]s completion bash > ~/.local/share/bash-completion/completions/%[1]s` once."},
	"fish": {"%[1]s completion fish | source",
		"`%[1]s completion fish > ~/.config/fish/completions/%[1]s.fish` once."},
	"powershell": {"%[1]s completion powershell | Out-String | Invoke-Expression",
		"`%[1]s completion powershell | Out-String | Invoke-Expression` in the PowerShell profile, `$PROFILE`."},
	"zsh": {"source <(%[1]s completion zsh)",
		"With completion enabled by `autoload -U compinit; compinit` in `~/.zshrc`, `%[1]s completion zsh > \"${fpath[1]}/_%[1]s\"` once."},
}

// flagTable is a section of a page that lists flags, each with an anchor of its name, its type,
// its default, or required for a flag cobra requires, and its description. A flag's help names a
// default that is no value of the flag, such as a file found in the current directory, at its
// end, as cobra's help would.
func flagTable(heading string, flags *pflag.FlagSet) string {
	var rows []string
	flags.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Deprecated != "" || f.Name == "help" {
			return
		}
		_, usage := pflag.UnquoteUsage(f)
		def := "—"
		if r := f.Annotations[cobra.BashCompOneRequiredFlag]; len(r) > 0 && r[0] == "true" {
			def = "required"
		} else if m := describedDefault.FindStringSubmatch(usage); m != nil {
			usage, def = strings.TrimSuffix(usage, m[0]), prose(m[1])
		} else if f.DefValue != "" && f.DefValue != "[]" {
			def = tableCell(code(f.DefValue))
		}
		name := fmt.Sprintf("`--%s`{ #flag-%s }", f.Name, f.Name)
		if f.Shorthand != "" {
			name += fmt.Sprintf(", `-%s`", f.Shorthand)
		}
		rows = append(rows, fmt.Sprintf("| %s | %s | %s | %s |", name, flagType(f), def, tableCell(prose(sentence(usage)))))
	})
	if len(rows) == 0 {
		return ""
	}
	return fmt.Sprintf("## %s\n\n| Flag | Type | Default | Description |\n| --- | --- | --- | --- |\n%s\n\n", heading, strings.Join(rows, "\n"))
}

// describedDefault is the default a flag's help names at its end.
var describedDefault = regexp.MustCompile(`\s*\(default ([^()]*)\)$`)

// flagTypes are the types of flags in the words of the page of config kinds, by pflag's names.
var flagTypes = map[string]string{
	"bool":           "boolean",
	"duration":       "duration",
	"int":            "integer",
	"string":         "string",
	"strings":        "list of string",
	"stringArray":    "list of string",
	"stringSlice":    "list of string",
	"stringToString": "map of string",
}

func flagType(f *pflag.Flag) string {
	if t, ok := flagTypes[f.Value.Type()]; ok {
		return t
	}
	return f.Value.Type()
}

// sentence is a flag's help as a sentence of a table: with a capital and a full stop.
func sentence(s string) string {
	if s == "" {
		return s
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// examples are a command's examples without the two spaces of help's indent.
func examples(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, line := range lines {
		lines[i] = dedent(line, 2)
	}
	return strings.Join(lines, "\n")
}

// collapsed reports whether cmd is a program's completion command, whose page holds every shell's
// command instead of a page for each.
func collapsed(cmd *cobra.Command) bool {
	return cmd.Name() == "completion" && cmd.HasParent() && !cmd.Parent().HasParent()
}

// available are the commands below cmd a user can run.
func available(cmd *cobra.Command) []*cobra.Command {
	var cmds []*cobra.Command
	for _, c := range cmd.Commands() {
		if c.IsAvailableCommand() && !c.IsAdditionalHelpTopicCommand() {
			cmds = append(cmds, c)
		}
	}
	return cmds
}

// documented are the commands below cmd that get pages of their own.
func documented(cmd *cobra.Command) []*cobra.Command {
	if collapsed(cmd) {
		return nil
	}
	return available(cmd)
}

// pageName is the file of a command's page: its command path joined with underscores.
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

// prose escapes placeholders such as <node> in the help's prose, so they show instead of being
// taken for HTML. Code blocks, fenced or indented, stay as they are.
func prose(text string) string {
	lines := strings.Split(text, "\n")
	fenced := false
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "```"):
			fenced = !fenced
		case fenced, strings.HasPrefix(line, "\t"), strings.HasPrefix(line, "    "):
		default:
			lines[i] = tag.ReplaceAllString(line, "&lt;$1")
		}
	}
	return strings.Join(lines, "\n")
}

// siteLink is a link of help to a page of the site, which help names by its URL, so it reads in a
// terminal: the page's path below the docs directory without .md, and an anchor. A section's index
// page has a URL that looks like a page's of that name, so help links the pages of a section and
// not its index.
var siteLink = regexp.MustCompile(`https://trevex\.github\.io/chalkos/((?:[a-z0-9-]+/)*)(#[a-z0-9-]+)?`)

// siteLinks makes the links of help to the site links to its pages in docs, titled by the heading
// the anchor names or else by the page's title. A page or anchor that does not exist is an error.
func siteLinks(text, docs string) (string, error) {
	var err error
	out := siteLink.ReplaceAllStringFunc(text, func(url string) string {
		m := siteLink.FindStringSubmatch(url)
		page := "index"
		if m[1] != "" {
			page = strings.TrimSuffix(m[1], "/")
		}
		title, e := linkTitle(filepath.Join(docs, page+".md"), strings.TrimPrefix(m[2], "#"))
		if e != nil && err == nil {
			err = fmt.Errorf("help links %s: %w", url, e)
		}
		return fmt.Sprintf("[%s](../../%s.md%s)", title, page, m[2])
	})
	return out, err
}

// The front matter's title, a heading with its text and an explicit anchor, and the Markdown of
// code and links in a heading's text.
var (
	titleLine  = regexp.MustCompile(`(?m)^title:\s*"?(.*?)"?\s*$`)
	heading    = regexp.MustCompile(`(?m)^#{2,6}\s+(.*?)(?:\s*\{\s*#([A-Za-z0-9_-]+)\s*\})?\s*$`)
	inlineLink = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	nonSlug    = regexp.MustCompile(`[^a-z0-9_\s-]`)
	slugBreak  = regexp.MustCompile(`[\s-]+`)
)

// linkTitle is the text of the heading of the page at path whose anchor is anchor, or the page's
// title without an anchor. A heading's anchor is its explicit one, or its text as the site's table
// of contents makes it a slug.
func linkTitle(path, anchor string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if anchor == "" {
		if m := titleLine.FindSubmatch(data); m != nil {
			return string(m[1]), nil
		}
		return "", fmt.Errorf("%s has no title", path)
	}
	for _, m := range heading.FindAllStringSubmatch(string(data), -1) {
		text := strings.ReplaceAll(inlineLink.ReplaceAllString(m[1], "$1"), "`", "")
		id := m[2]
		if id == "" {
			id = slugBreak.ReplaceAllString(strings.TrimSpace(nonSlug.ReplaceAllString(strings.ToLower(text), "")), "-")
		}
		if id == anchor {
			return text, nil
		}
	}
	return "", fmt.Errorf("%s has no heading #%s", path, anchor)
}
