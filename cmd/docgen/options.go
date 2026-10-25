package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// optionSection is a section of an options page: its heading, its anchor, the line that opens it
// and the prefixes of the options it holds. An empty prefix holds every option of its input.
type optionSection struct {
	heading, anchor, intro string
	prefixes               []string
}

// optionPage is a page of options: its file, its sections and its closing line. Its opening comes
// from the templates directory, as <name>.head.md, and an optional <name>.tail.md follows its
// sections.
type optionPage struct {
	name     string
	sections []optionSection
	sources  string
}

// argumentsPage holds the arguments of mkImages, every option of the arguments' JSON.
var argumentsPage = optionPage{
	name: "mkimages",
	sections: []optionSection{{
		heading:  "mkImages arguments",
		anchor:   "mkimages-arguments",
		intro:    "The arguments `chalkos.lib.mkImages` takes, each checked as a module option.",
		prefixes: []string{""},
	}},
	sources: "Sources: the options of `lib/images-arguments.nix`, which check mkImages's arguments,\nthrough nixosOptionsDoc.\n",
}

// imagePages hold the options of an image's node modules. Every option of the options' JSON must
// fall into one of their sections, so a new option is placed before the pages build.
var imagePages = []optionPage{
	{
		name: "image-options",
		sections: []optionSection{
			{"Disk", "disk", "The sizes of the partitions an image's build lays out.", []string{"chalkos.disk"}},
			{"Kernel", "kernel", "The kernel modules an image carries.", []string{"chalkos.kernel"}},
			{"Kubernetes and CNI", "kubernetes-and-cni", "Whether an image runs Kubernetes, the packages and images it runs it with, and its CNI.", []string{"chalkos.kubernetes", "chalkos.cni"}},
			{"Router and forwarding", "router-and-forwarding", "Whether an image carries the router layer, and whether its node forwards packets between its interfaces.", []string{"chalkos.router", "chalkos.forwarding"}},
			{"Upgrade and health", "upgrade-and-health", "How many times systemd-boot boots a new image before it falls back, and when a boot of it is healthy.", []string{"chalkos.upgrade"}},
			{"Debug and maintenance", "debug-and-maintenance", "The debug tools an image carries, and the OS CA whose clients its maintenance mode accepts.", []string{"chalkos.debug", "chalkos.maintenance"}},
			{"Set by mkImages", "set-by-mkimages", "mkImages sets these from its arguments; a module reads them rather than setting them.", []string{"chalkos.variant", "chalkos.platform", "chalkos.provenance"}},
		},
		sources: "Sources: the option declarations of the node modules in `modules/node/`, through\nnixosOptionsDoc.\n",
	},
	{
		name: "extension-api",
		sections: []optionSection{
			{"Kinds", "kinds", "The config kinds an image accepts, and the kinds a unit reads.", []string{"chalkos.kinds", "systemd.services.<name>.chalkos.kinds"}},
			{"Facts", "facts", "The values a node reads at a delivery, which change rules and effects compare.", []string{"chalkos.facts"}},
			{"Outputs", "outputs", "The files a node renders from its documents at each delivery.", []string{"chalkos.outputs"}},
			{"Effects", "effects", "What a delivery does to units when outputs, documents or facts change.", []string{"chalkos.effects"}},
		},
		sources: "Sources: the option declarations of the node modules in `modules/node/`, through\nnixosOptionsDoc, and `lib/chalkos.nix`.\n",
	},
}

// option is an option as nixosOptionsDoc's optionsJSON holds it.
type option struct {
	name         string
	Declarations []struct{ Name, URL string }
	Default      *literal
	Description  string
	Example      *literal
	ReadOnly     bool
	Type         string
}

// literal is a default or an example: a Nix expression, or Markdown for literalMD.
type literal struct {
	Type string `json:"_type"`
	Text string
}

// writeOptions writes the pages of mkImages's arguments, from the optionsJSON at arguments, and of
// the image options and the extension API, from the optionsJSON at options, into dir, each
// opened with its head from templates.
func writeOptions(dir, templates, arguments, options string) error {
	args, err := readOptions(arguments)
	if err != nil {
		return err
	}
	opts, err := readOptions(options)
	if err != nil {
		return err
	}
	placed := map[string]bool{}
	pages := []struct {
		page optionPage
		opts []option
	}{{argumentsPage, args}}
	for _, p := range imagePages {
		pages = append(pages, struct {
			page optionPage
			opts []option
		}{p, opts})
	}
	for _, p := range pages {
		body, err := renderOptions(p.page, p.opts, templates, placed)
		if err != nil {
			return fmt.Errorf("%s: %w", p.page.name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, p.page.name+".md"), []byte(body), 0o644); err != nil {
			return err
		}
	}
	var unplaced []string
	for _, o := range opts {
		if !placed[o.name] {
			unplaced = append(unplaced, o.name)
		}
	}
	if len(unplaced) > 0 {
		return fmt.Errorf("no section of the options pages holds %s; add its prefix to imagePages in cmd/docgen/options.go", strings.Join(unplaced, ", "))
	}
	return nil
}

// readOptions reads an optionsJSON, its options by name.
func readOptions(path string) ([]option, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var byName map[string]option
	if err := json.Unmarshal(data, &byName); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var opts []option
	for name, o := range byName {
		o.name = name
		opts = append(opts, o)
	}
	slices.SortFunc(opts, func(a, b option) int { return strings.Compare(a.name, b.name) })
	return opts, nil
}

// renderOptions renders a page: its head, a section per section with its options, its tail and
// its sources. placed records the options of opts that a section holds; one section only holds
// each.
func renderOptions(p optionPage, opts []option, templates string, placed map[string]bool) (string, error) {
	head, err := os.ReadFile(filepath.Join(templates, p.name+".head.md"))
	if err != nil {
		return "", err
	}
	tail, err := os.ReadFile(filepath.Join(templates, p.name+".tail.md"))
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	var b strings.Builder
	b.Write(head)
	anchors := map[string]bool{}
	for _, s := range p.sections {
		var held []option
		for _, o := range opts {
			if !placed[o.name] && slices.ContainsFunc(s.prefixes, func(prefix string) bool { return under(o.name, prefix) }) {
				held = append(held, o)
			}
		}
		if len(held) == 0 {
			return "", fmt.Errorf("the section %s holds no option", s.heading)
		}
		fmt.Fprintf(&b, "\n## %s { #%s }\n\n%s\n", s.heading, s.anchor, s.intro)
		anchors[s.anchor] = true
		// An option within another of the section, as a submodule's, is a row of that one's
		// table. Options are sorted by name, so an option comes before those within it.
		subs := map[string][]option{}
		var tops []option
		for _, o := range held {
			placed[o.name] = true
			if i := slices.IndexFunc(tops, func(t option) bool { return under(o.name, t.name) }); i >= 0 {
				subs[tops[i].name] = append(subs[tops[i].name], o)
				continue
			}
			tops = append(tops, o)
		}
		// Options of the section's prefixes in the order the prefixes are given.
		slices.SortStableFunc(tops, func(a, b option) int { return prefixIndex(s, a.name) - prefixIndex(s, b.name) })
		for _, o := range tops {
			for _, x := range append([]option{o}, subs[o.name]...) {
				a := optionAnchor(x.name)
				if anchors[a] {
					return "", fmt.Errorf("two entries would share the anchor #%s", a)
				}
				anchors[a] = true
			}
			writeOption(&b, o, subs[o.name])
		}
	}
	b.Write(tail)
	b.WriteString("\n" + p.sources)
	return b.String(), nil
}

// under tells whether the option name is prefix or within it.
func under(name, prefix string) bool {
	return prefix == "" || name == prefix || strings.HasPrefix(name, prefix+".")
}

func prefixIndex(s optionSection, name string) int {
	return slices.IndexFunc(s.prefixes, func(prefix string) bool { return under(name, prefix) })
}

// optionAnchor is an option's anchor: its name in lower case with the letters, digits, - and _
// alone, as the site slugged the option's heading when one page held every option.
func optionAnchor(name string) string {
	name = strings.ReplaceAll(name, "<name>", "")
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// writeOption writes an option's entry: its heading, its type, default and declaration, its
// description and its example, then the table of the options within it.
func writeOption(b *strings.Builder, o option, subs []option) {
	fmt.Fprintf(b, "\n### `%s` { #%s }\n\n", o.name, optionAnchor(o.name))
	var decls []string
	for _, d := range o.Declarations {
		decls = append(decls, fmt.Sprintf("[%s](%s)", d.Name, d.URL))
	}
	fmt.Fprintf(b, "Type: %s · Default: %s · Declared in: %s\n", typeCell(o), defaultCell(o), strings.Join(decls, ", "))
	if d := strings.TrimSpace(o.Description); d != "" {
		fmt.Fprintf(b, "\n%s\n", d)
	}
	if o.Example != nil {
		if inline, ok := inlineLiteral(o.Example, 40); ok {
			fmt.Fprintf(b, "\nExample: %s\n", inline)
		} else {
			fmt.Fprintf(b, "\nExample:\n\n%s", literalBlock(o.Example))
		}
	}
	if len(subs) == 0 {
		return
	}
	fmt.Fprintf(b, "\nThe options within each, by their path below `%s`:\n\n", o.name)
	b.WriteString("| Option | Type | Default | Description |\n| --- | --- | --- | --- |\n")
	var blocks []option
	for _, s := range subs {
		desc := tableCell(strings.Join(strings.Fields(s.Description), " "))
		if s.Example != nil {
			if inline, ok := inlineLiteral(s.Example, 80); ok {
				desc = strings.TrimSpace(desc + " Example: " + tableCell(inline))
			} else {
				blocks = append(blocks, s)
			}
		}
		fmt.Fprintf(b, "| `%s`{ #%s } | %s | %s | %s |\n",
			strings.TrimPrefix(s.name, o.name+"."), optionAnchor(s.name), tableCell(typeCell(s)), tableCell(defaultCell(s)), desc)
	}
	for _, s := range blocks {
		fmt.Fprintf(b, "\nExample of `%s`:\n\n%s", strings.TrimPrefix(s.name, o.name+"."), literalBlock(s.Example))
	}
}

// typeCell is an option's type in the kinds page's words, and whether it is read-only.
func typeCell(o option) string {
	t := optionType(o.Type)
	if o.ReadOnly {
		t += ", read-only"
	}
	return t
}

// defaultCell is an option's default on one line, "required" for an option to be set that has
// none, or "—" for a read-only one without a default.
func defaultCell(o option) string {
	switch {
	case o.Default != nil:
		if o.Default.Type == "literalMD" {
			return strings.Join(strings.Fields(o.Default.Text), " ")
		}
		return code(strings.Join(strings.Fields(o.Default.Text), " "))
	case o.ReadOnly:
		return "—"
	}
	return "required"
}

// inlineLiteral is an example on one line, when it fits on one: an expression without comments
// whose lines join into one of at most limit characters.
func inlineLiteral(l *literal, limit int) (string, bool) {
	if l.Type == "literalMD" {
		return strings.Join(strings.Fields(l.Text), " "), true
	}
	joined := strings.Join(strings.Fields(l.Text), " ")
	if strings.Contains(l.Text, "#") || strings.Contains(l.Text, "''") || len(joined) > limit {
		return "", false
	}
	return code(joined), true
}

// literalBlock is an example as a block: Markdown as it is, an expression as Nix code.
func literalBlock(l *literal) string {
	if l.Type == "literalMD" {
		return strings.TrimSpace(l.Text) + "\n"
	}
	return "```nix\n" + strings.TrimRight(l.Text, "\n") + "\n```\n"
}

// code is s in a code span, delimited by more backticks than it holds in a row.
func code(s string) string {
	fence := "`"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	if fence != "`" {
		return fence + " " + s + " " + fence
	}
	return fence + s + fence
}

var (
	quoted        = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	singularEnum  = regexp.MustCompile(`^value "((?:[^"\\]|\\.)*)" \(singular enum\)$`)
	evaluatesTo   = regexp.MustCompile(`^function that evaluates to a\(n\) (.*)$`)
	concatenated  = regexp.MustCompile(`^strings concatenated with "(?:[^"\\]|\\.)*"$`)
	patternString = regexp.MustCompile(`^string matching the pattern (.*)$`)
)

// optionType rewrites a type as nixpkgs describes it in the words of the kinds page: null last,
// "map" for an attribute set, values of an enum in code, and patterns in code.
func optionType(t string) string {
	t = strings.TrimSpace(t)
	if rest, ok := strings.CutPrefix(t, "null or "); ok {
		return optionType(rest) + " or null"
	}
	// (A) or B, as nixpkgs writes types.either of a composite A.
	if strings.HasPrefix(t, "(") {
		if end := closing(t); end > 0 {
			inner, rest := t[1:end], t[end+1:]
			if rest == "" {
				return optionType(inner)
			}
			if after, ok := strings.CutPrefix(rest, " or "); ok {
				return optionType(inner) + " or " + optionType(after)
			}
		}
	}
	for from, to := range map[string]string{"list of ": "list of ", "attribute set of ": "map of ", "non-empty list of ": "non-empty list of "} {
		if rest, ok := strings.CutPrefix(t, from); ok {
			return to + optionType(rest)
		}
	}
	if rest, ok := strings.CutPrefix(t, "one of "); ok {
		return "one of " + quoted.ReplaceAllString(rest, "`$1`")
	}
	if m := singularEnum.FindStringSubmatch(t); m != nil {
		return "`" + m[1] + "`"
	}
	if m := evaluatesTo.FindStringSubmatch(t); m != nil {
		return "function returning " + optionType(m[1])
	}
	if concatenated.MatchString(t) {
		return "string"
	}
	if m := patternString.FindStringSubmatch(t); m != nil {
		return "string matching " + code(m[1])
	}
	switch t {
	case "submodule":
		return "submodule"
	case "raw value", "unspecified value", "anything":
		return "any"
	case "absolute path":
		return "path"
	case "positive integer, meaning >0":
		return "integer of at least 1"
	case "unsigned integer, meaning >=0":
		return "integer of at least 0"
	case "signed integer":
		return "integer"
	}
	return t
}

// closing is the index of the parenthesis that closes the one t starts with, or -1.
func closing(t string) int {
	depth := 0
	for i, r := range t {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// tableCell escapes the pipes of a table cell's text outside its code spans, and what Markdown
// would take for an HTML tag there, as <path>, as prose does. The site's tables leave a pipe
// within a code span to the span, where an escape would show.
func tableCell(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '`' {
			n := 0
			for i+n < len(s) && s[i+n] == '`' {
				n++
			}
			fence := s[i : i+n]
			if end := strings.Index(s[i+n:], fence); end >= 0 {
				b.WriteString(s[i : i+n+end+n])
				i += n + end + n
				continue
			}
			b.WriteString(fence)
			i += n
			continue
		}
		switch {
		case s[i] == '|':
			b.WriteString(`\|`)
		case tag.MatchString(s[i:min(i+2, len(s))]):
			b.WriteString("&lt;")
		default:
			b.WriteByte(s[i])
		}
		i++
	}
	return b.String()
}
