package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/effects"
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/rules"
)

// builtinsDescription is the description of the page of built-ins.
const builtinsDescription = "The change rules, facts, outputs and effects that chalkos's images declare"

// builtinsIntro opens the page of built-ins, after its front matter.
const builtinsIntro = "# Built-ins\n\n" +
	"This page is for image authors and operators who want to know why a node refused a config, or\n" +
	"what a delivery reloads and restarts. It lists the change rules, facts, outputs and effects that\n" +
	"chalkos's own modules declare: those of every image, and those that images with Kubernetes add.\n" +
	"An extension declares more with the [Extension API](extension-api.md), and its outputs and\n" +
	"effects take names other than these.\n"

// builtinsSources closes the page of built-ins.
const builtinsSources = "\nSources: the schemas, `facts.json`, `outputs.json` and `effects.json` that an image holds in\n" +
	"`/etc/chalkos`, of an image of the default variant and of one with `kubernetes = false`, which\n" +
	"the node modules in `modules/node/` declare.\n"

// The openings of the page's sections.
const (
	rulesIntro = "A change rule refuses a config that breaks it before the node applies any of it.\n" +
		"[Guard changes with rules and facts](../build/guard-changes.md) describes how rules are written\n" +
		"and where they run. Each table holds a kind's rules: the field a field rule sits on, or `—` for\n" +
		"a kind rule, which sees all of the kind's documents; what the rule guards; what a change it\n" +
		"refuses takes, `refuse`, `reset` or `reinstall`; the CEL expression, which holds for a config\n" +
		"the rule accepts; and the message, or the CEL expression that builds it. An expression too long\n" +
		"for its row follows the table.\n"
	factsIntro = "A fact is node state that change rules and the `when` of effects read as `facts.<name>`. A\n" +
		"file fact is the JSON content of a file, and null while the file is missing; a command fact is\n" +
		"the JSON a program prints.\n"
	outputsIntro = "An output is a set of config files that chalkd renders from the node's documents at every\n" +
		"delivery and boot. [Render config files](../build/render-config.md) describes them. Each table\n" +
		"gives the kinds an output reads, the directory services read its files in, its renderer, and\n" +
		"whether a check runs on the rendered files before a delivery changes anything.\n"
	effectsIntro = "An effect is what a delivery does to the node's services once it is applied.\n" +
		"[React to changes](../build/react-to-changes.md) describes them and the order chalkd takes them\n" +
		"in. A `when` too long for its row follows the table.\n"
)

// builtinRule is a change rule of a kind: of the kind itself, whose field is empty, or of one of its
// fields.
type builtinRule struct {
	rules.Rule
	field string
}

// kindRules are the change rules of a kind.
type kindRules struct {
	kind  kindSchema
	rules []builtinRule
}

// builtinImage is what an image's rules image holds of its built-ins: its kinds' rules, in the
// order of the page of config kinds, and its facts, outputs and effects by name.
type builtinImage struct {
	kinds   []kindRules
	facts   map[string]facts.Definition
	outputs map[string]outputs.Definition
	effects map[string]effects.Definition
}

// writeBuiltins writes the page of built-ins to page from two rules images, as an image's
// system.build.chalkosRulesImage lays them out: that of the default variant, at def, and that of
// an image without Kubernetes, at plain. A built-in of the plain image is one of every image; one
// the default image alone has is one of images with Kubernetes.
func writeBuiltins(page, def, plain string) error {
	d, err := readBuiltinImage(def)
	if err != nil {
		return err
	}
	p, err := readBuiltinImage(plain)
	if err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString(frontMatter("Built-ins", builtinsDescription))
	b.WriteString(builtinsIntro)

	everyRules, kubeRules, err := splitRules(d.kinds, p.kinds)
	if err != nil {
		return err
	}
	fmt.Fprintf(&b, "\n## Change rules { #change-rules }\n\n%s", rulesIntro)
	taken := map[string]bool{}
	writeRulesSection(&b, "rules-every-image", "The rules of the kinds every image accepts.", everyRules, taken)
	writeRulesSection(&b, "rules-kubernetes", "The rules of the kinds that images with Kubernetes accept, and those they add to other kinds.", kubeRules, taken)

	everyFacts, kubeFacts, err := split("fact", d.facts, p.facts)
	if err != nil {
		return err
	}
	fmt.Fprintf(&b, "\n## Facts { #facts }\n\n%s", factsIntro)
	writeFacts(&b, "facts-every-image", "The facts every image declares.", everyFacts, d)
	writeFacts(&b, "facts-kubernetes", "The facts images with Kubernetes add.", kubeFacts, d)

	everyOutputs, kubeOutputs, err := split("output", d.outputs, p.outputs)
	if err != nil {
		return err
	}
	fmt.Fprintf(&b, "\n## Outputs { #outputs }\n\n%s", outputsIntro)
	writeOutputs(&b, "outputs-every-image", "The outputs every image renders.", everyOutputs, d.outputs)
	writeOutputs(&b, "outputs-kubernetes", "The outputs images with Kubernetes add.", kubeOutputs, d.outputs)

	everyEffects, kubeEffects, err := split("effect", d.effects, p.effects)
	if err != nil {
		return err
	}
	fmt.Fprintf(&b, "\n## Effects { #effects }\n\n%s", effectsIntro)
	writeEffects(&b, "effects-every-image", "The effects every image declares.", everyEffects, d.effects)
	writeEffects(&b, "effects-kubernetes", "The effects images with Kubernetes add.", kubeEffects, d.effects)

	b.WriteString(builtinsSources)
	return os.WriteFile(page, []byte(b.String()), 0o644)
}

// readBuiltinImage reads the schemas, facts.json, outputs.json and effects.json of a rules image.
func readBuiltinImage(dir string) (builtinImage, error) {
	var img builtinImage
	entries, err := os.ReadDir(filepath.Join(dir, "schemas"))
	if err != nil {
		return img, err
	}
	var kinds []kindSchema
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		k, err := readKindSchema(filepath.Join(dir, "schemas", e.Name()))
		if err != nil {
			return img, err
		}
		kinds = append(kinds, k)
	}
	slices.SortFunc(kinds, compareKinds)
	for _, k := range kinds {
		ruled, err := schemaRules(k)
		if err != nil {
			return img, err
		}
		if len(ruled) > 0 {
			img.kinds = append(img.kinds, kindRules{k, ruled})
		}
	}
	// The readers take a missing file for an image that declares none, but a rules image holds all
	// three.
	for _, name := range []string{"facts.json", "outputs.json", "effects.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return img, err
		}
	}
	if img.facts, err = facts.Read(filepath.Join(dir, "facts.json")); err != nil {
		return img, err
	}
	if img.outputs, err = outputs.Read(filepath.Join(dir, "outputs.json"), outputs.DefaultRoot); err != nil {
		return img, err
	}
	if img.effects, err = effects.Read(filepath.Join(dir, "effects.json")); err != nil {
		return img, err
	}
	return img, nil
}

// schemaRules returns a kind's rules: its own, in x-chalkos-kind, then its fields', in the order of
// their paths.
func schemaRules(k kindSchema) ([]builtinRule, error) {
	own, err := decodeRules(asMap(k.root["x-chalkos-kind"])["rules"], "")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", k.path, err)
	}
	fieldRules, err := ruledFields(k.root, "spec", resolve(k.root, asMap(props(k.root)["spec"])))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", k.path, err)
	}
	return append(own, fieldRules...), nil
}

// ruledFields returns the rules of the schema s, of the field at path, and of the fields within it,
// written as on the page of config kinds.
func ruledFields(root map[string]any, path string, s map[string]any) ([]builtinRule, error) {
	s = resolve(root, s)
	rules, err := decodeRules(s["x-chalkos-rules"], path)
	if err != nil {
		return nil, err
	}
	if s["x-chalkos-secret-ref"] == true || s["x-chalkos-kubernetes-object"] == true {
		return rules, nil
	}
	var inner [][]builtinRule
	p := props(s)
	for _, name := range slices.Sorted(maps.Keys(p)) {
		r, err := ruledFields(root, path+"."+name, asMap(p[name]))
		if err != nil {
			return nil, err
		}
		inner = append(inner, r)
	}
	if v, ok := s["additionalProperties"].(map[string]any); ok {
		r, err := ruledFields(root, path+".<name>", v)
		if err != nil {
			return nil, err
		}
		inner = append(inner, r)
	}
	if items, ok := s["items"].(map[string]any); ok {
		r, err := ruledFields(root, path+"[]", items)
		if err != nil {
			return nil, err
		}
		inner = append(inner, r)
	}
	for _, branch := range asSlice(s["anyOf"]) {
		r, err := ruledFields(root, path, asMap(branch))
		if err != nil {
			return nil, err
		}
		inner = append(inner, r)
	}
	return slices.Concat(append([][]builtinRule{rules}, inner...)...), nil
}

// decodeRules decodes the rules of an x-chalkos-rules, or of x-chalkos-kind's rules, of the field
// at field.
func decodeRules(v any, field string) ([]builtinRule, error) {
	if v == nil {
		return nil, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var decoded []rules.Rule
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, fmt.Errorf("the rules of %s: %w", cmp.Or(field, "the kind"), err)
	}
	out := make([]builtinRule, len(decoded))
	for i, r := range decoded {
		r.Takes = cmp.Or(r.Takes, rules.Refuse)
		out[i] = builtinRule{Rule: r, field: field}
	}
	return out, nil
}

// compareKinds orders kinds as the page of config kinds does: chalkos.dev's first, then by group
// and kind.
func compareKinds(a, b kindSchema) int {
	if (a.group == "chalkos.dev") != (b.group == "chalkos.dev") {
		if a.group == "chalkos.dev" {
			return -1
		}
		return 1
	}
	return strings.Compare(a.group+"\x00"+a.kind, b.group+"\x00"+b.kind)
}

// splitRules splits the rules of the default image's kinds into those of every image, which the
// plain image has as well, and those of images with Kubernetes. A rule of the plain image that the
// default image lacks fails it.
func splitRules(def, plain []kindRules) (every, kube []kindRules, err error) {
	inDefault := map[string][]builtinRule{}
	for _, k := range def {
		inDefault[k.kind.kind] = k.rules
	}
	inPlain := map[string][]builtinRule{}
	for _, k := range plain {
		inPlain[k.kind.kind] = k.rules
		for _, r := range k.rules {
			if !slices.Contains(inDefault[k.kind.kind], r) {
				return nil, nil, fmt.Errorf("the image without Kubernetes has a rule of %s that the default image lacks: %s", k.kind.kind, r.Rule.Rule)
			}
		}
	}
	for _, k := range def {
		var shared, added []builtinRule
		for _, r := range k.rules {
			if slices.Contains(inPlain[k.kind.kind], r) {
				shared = append(shared, r)
			} else {
				added = append(added, r)
			}
		}
		if len(shared) > 0 {
			every = append(every, kindRules{k.kind, shared})
		}
		if len(added) > 0 {
			kube = append(kube, kindRules{k.kind, added})
		}
	}
	return every, kube, nil
}

// split splits the default image's built-ins of a sort into those of every image, which the plain
// image has alike, and those of images with Kubernetes, each by name. A built-in of the plain image
// that the default image lacks, or has otherwise, fails it.
func split[T any](sort string, def, plain map[string]T) (every, kube []string, err error) {
	for _, name := range slices.Sorted(maps.Keys(plain)) {
		d, ok := def[name]
		if !ok || !reflect.DeepEqual(d, plain[name]) {
			return nil, nil, fmt.Errorf("the image without Kubernetes has the %s %s, which the default image lacks or declares otherwise", sort, name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(def)) {
		if _, ok := plain[name]; ok {
			every = append(every, name)
		} else {
			kube = append(kube, name)
		}
	}
	return every, kube, nil
}

// inlineLimit is the length of the longest CEL expression a table's row holds; longer ones, and
// those of several lines, follow the table.
const inlineLimit = 100

func inline(expr string) bool { return len(expr) <= inlineLimit && !strings.Contains(expr, "\n") }

// textBlock is s as a plain code block, fenced with more backticks than it holds in a row.
func textBlock(s string) string {
	fence := "```"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	return fence + "text\n" + strings.TrimRight(s, "\n") + "\n" + fence + "\n"
}

// writeRulesSection writes a section of change rules: a heading and a table per kind. A kind's
// anchor is #rules-<kind>, or #rules-kubernetes-<kind> for the rules images with Kubernetes add to a
// kind of every image, whose anchors are in taken.
func writeRulesSection(b *strings.Builder, anchor, intro string, kinds []kindRules, taken map[string]bool) {
	heading := "Every image"
	if anchor == "rules-kubernetes" {
		heading = "Images with Kubernetes"
	}
	fmt.Fprintf(b, "\n### %s { #%s }\n\n", heading, anchor)
	if len(kinds) == 0 {
		b.WriteString("None.\n")
		return
	}
	b.WriteString(intro + "\n")
	for _, k := range kinds {
		id := "rules-" + strings.ToLower(k.kind.kind)
		if taken[id] {
			id = "rules-kubernetes-" + strings.ToLower(k.kind.kind)
		}
		taken[id] = true
		fmt.Fprintf(b, "\n#### %s { #%s }\n\n", k.kind.kind, id)
		b.WriteString("| Rule | Field | Guards | Takes | Expression | Message |\n| --- | --- | --- | --- | --- | --- |\n")
		var after []string
		for i, r := range k.rules {
			n := i + 1
			field := "—"
			if r.field != "" {
				field = code(r.field)
			}
			expr := "after the table"
			if inline(r.Rule.Rule) {
				expr = code(r.Rule.Rule)
			} else {
				after = append(after, fmt.Sprintf("\nExpression of rule %d:\n\n%s", n, textBlock(r.Rule.Rule)))
			}
			var msg string
			switch {
			case r.MessageExpression == "":
				msg = strings.Join(strings.Fields(r.Message), " ")
			case inline(r.MessageExpression):
				msg = code(r.MessageExpression)
			default:
				msg = "after the table"
				after = append(after, fmt.Sprintf("\nMessage of rule %d:\n\n%s", n, textBlock(r.MessageExpression)))
			}
			guards := "—"
			if r.Description != "" {
				guards = tableCell(strings.Join(strings.Fields(r.Description), " "))
			}
			fmt.Fprintf(b, "| %d | %s | %s | %s | %s | %s |\n", n, field, guards, code(string(r.Takes)), tableCell(expr), tableCell(msg))
		}
		b.WriteString(strings.Join(after, ""))
	}
}

// storePath is a path of the Nix store, which the page shows by its last component without the
// hash.
var storePath = regexp.MustCompile(`/nix/store/[0-9a-z]{32}-[^\s"']*`)

// stripStore shows each store path of a command by the name of its program or file.
func stripStore(s string) string {
	return storePath.ReplaceAllStringFunc(s, func(p string) string {
		return path.Base(strings.TrimPrefix(p, "/nix/store/")[33:])
	})
}

var (
	readFact = regexp.MustCompile(`\b(?:facts|oldFacts)\.([a-z][A-Za-z0-9]*)`)
	readKind = regexp.MustCompile(`\b(?:config|oldConfig)\["[^"]*"\]\.([A-Z][A-Za-z0-9]*)`)
)

// matches are the names the first group of re matches in exprs, sorted and without repeats.
func matches(re *regexp.Regexp, exprs ...string) []string {
	var names []string
	for _, e := range exprs {
		for _, m := range re.FindAllStringSubmatch(e, -1) {
			names = append(names, m[1])
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// andList joins items as a list in a sentence: "a", "a and b", "a, b and c".
func andList(items []string, conj string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + conj + " " + items[len(items)-1]
}

func codes(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = code(s)
	}
	return out
}

// writeFacts writes a section of facts, and which of the default image's kinds and effects read
// each.
func writeFacts(b *strings.Builder, anchor, intro string, names []string, img builtinImage) {
	writeSubsection(b, anchor, intro, len(names))
	if len(names) == 0 {
		return
	}
	b.WriteString("| Fact | Reads | Read by |\n| --- | --- | --- |\n")
	for _, name := range names {
		f := img.facts[name]
		var reads string
		switch {
		case f.Command != "":
			reads = "what " + code(stripStore(f.Command)) + " prints"
			if len(f.Inputs) > 0 {
				var inputs []string
				for _, in := range f.Inputs {
					if in == "fallbackSecret" {
						inputs = append(inputs, "the fallback secret")
					} else {
						inputs = append(inputs, code(in))
					}
				}
				reads += ", given " + andList(inputs, "and")
			}
		case len(f.Fields) > 0:
			key := "key"
			if len(f.Fields) > 1 {
				key = "keys"
			}
			reads = fmt.Sprintf("the %s %s of the file %s", key, andList(codes(f.Fields), "and"), code(f.File))
			if f.HashOthers {
				reads += ", and the SHA-256 of each other key"
			}
		case f.HashOthers:
			reads = "the SHA-256 of each key of the file " + code(f.File)
		default:
			reads = "file " + code(f.File)
		}
		var kinds, effects []string
		for _, k := range img.kinds {
			for _, r := range k.rules {
				if slices.Contains(matches(readFact, r.Rule.Rule, r.MessageExpression), name) {
					kinds = append(kinds, k.kind.kind)
					break
				}
			}
		}
		for _, e := range slices.Sorted(maps.Keys(img.effects)) {
			if slices.Contains(matches(readFact, img.effects[e].When), name) {
				effects = append(effects, e)
			}
		}
		var by []string
		if len(kinds) > 0 {
			by = append(by, "the rules of "+andList(codes(kinds), "and"))
		}
		if len(effects) > 0 {
			by = append(by, "the effects "+andList(codes(effects), "and"))
		}
		readBy := "—"
		if len(by) > 0 {
			readBy = sentence(strings.Join(by, ", and "))
		}
		fmt.Fprintf(b, "| `%s`{ #fact-%s } | %s | %s |\n", name, strings.ToLower(name), tableCell(sentence(reads)), readBy)
	}
}

// writeOutputs writes a section of outputs.
func writeOutputs(b *strings.Builder, anchor, intro string, names []string, defs map[string]outputs.Definition) {
	writeSubsection(b, anchor, intro, len(names))
	if len(names) == 0 {
		return
	}
	b.WriteString("| Output | Inputs | Directory | Renderer | Check |\n| --- | --- | --- | --- | --- |\n")
	for _, name := range names {
		o := defs[name]
		check := "no"
		if o.Check != "" {
			check = "yes"
		}
		fmt.Fprintf(b, "| `%s`{ #output-%s } | %s | %s | %s | %s |\n",
			name, name, strings.Join(codes(o.Inputs), ", "), code(o.Dir), tableCell(renderer(o.Render)), check)
	}
}

// renderer describes an output's render command: one of lib.chalkos.render's renderers, or a
// command.
func renderer(render string) string {
	words := strings.Fields(stripStore(render))
	switch {
	case len(words) == 4 && words[0] == "chalkos-render" && words[1] == "template":
		return "Go template " + code(words[2])
	case len(words) >= 2 && words[0] == "chalkos-render" && words[1] == "documents":
		return "The documents of " + andList(codes(words[2:]), "and")
	case len(words) == 1 && strings.HasPrefix(words[0], "chalkos-render-jq-"):
		return "jq program " + code(strings.TrimPrefix(words[0], "chalkos-render-jq-")+".jq")
	}
	return "Command " + code(strings.Join(words, " "))
}

// writeEffects writes a section of effects, each with when it fires and what it does.
func writeEffects(b *strings.Builder, anchor, intro string, names []string, defs map[string]effects.Definition) {
	writeSubsection(b, anchor, intro, len(names))
	if len(names) == 0 {
		return
	}
	b.WriteString("| Effect | Fires when | Does |\n| --- | --- | --- |\n")
	var after []string
	for _, name := range names {
		e := defs[name]
		var fires []string
		switch len(e.Outputs) {
		case 0:
		case 1:
			fires = append(fires, "the output "+code(e.Outputs[0])+" changed")
		default:
			fires = append(fires, "one of the outputs "+andList(codes(e.Outputs), "and")+" changed")
		}
		if len(e.Kinds) > 0 {
			fires = append(fires, "the documents of "+andList(codes(e.Kinds), "or")+", or a value of a Secret they reference, changed")
		}
		if e.When != "" {
			if inline(e.When) {
				fires = append(fires, code(e.When)+" holds")
			} else {
				var reads []string
				if kinds := matches(readKind, e.When); len(kinds) > 0 {
					reads = append(reads, "the documents of "+andList(codes(kinds), "and"))
				}
				if facts := matches(readFact, e.When); len(facts) > 0 {
					word := "fact"
					if len(facts) > 1 {
						word = "facts"
					}
					reads = append(reads, "the "+word+" "+andList(codes(facts), "and"))
				}
				w := "the `when` after the table holds"
				if len(reads) > 0 {
					w += ", which reads " + andList(reads, "and")
				}
				fires = append(fires, w)
				after = append(after, fmt.Sprintf("\n`when` of `%s`:\n\n%s", name, textBlock(e.When)))
			}
		}
		var does []string
		if len(e.Reload) > 0 {
			does = append(does, sentence("reloads "+andList(codes(e.Reload), "and")))
		}
		if e.Run != "" {
			does = append(does, sentence("runs "+code(stripStore(e.Run))))
		}
		if len(e.Restart) > 0 {
			s := "restarts " + andList(codes(e.Restart), "and")
			if e.Start {
				s += ", and starts them when they are stopped"
			}
			does = append(does, sentence(s))
		}
		switch e.Chalkd {
		case effects.ChalkdRestart:
			does = append(does, "Restarts chalkd's Kubernetes loops.")
		case effects.ChalkdReload:
			does = append(does, "Reloads chalkd's control-plane loop.")
		}
		fmt.Fprintf(b, "| `%s`{ #effect-%s } | %s | %s |\n",
			name, name, tableCell(sentence(strings.Join(fires, ", or "))), tableCell(strings.Join(does, " ")))
	}
	b.WriteString(strings.Join(after, ""))
}

// writeSubsection writes the heading of a section's built-ins of every image, or of images with
// Kubernetes, and its opening.
func writeSubsection(b *strings.Builder, anchor, intro string, n int) {
	heading := "Every image"
	if strings.HasSuffix(anchor, "-kubernetes") {
		heading = "Images with Kubernetes"
	}
	fmt.Fprintf(b, "\n### %s { #%s }\n\n", heading, anchor)
	if n == 0 {
		b.WriteString("None.\n")
		return
	}
	b.WriteString(intro + "\n\n")
}
