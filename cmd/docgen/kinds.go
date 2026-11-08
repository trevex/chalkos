package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

// kindsDescription is the description of the page of config kinds.
const kindsDescription = "The fields of the config kinds of chalkos's images, of the inventory and of the lab file"

// kindsIntro opens the page of config kinds, after its front matter.
const kindsIntro = "# Config kinds\n\n" +
	"This page is for operators who write a node's config documents, an inventory or a lab file. It\n" +
	"lists the config kinds that an image of `chalkos.lib.mkImages`'s default variant accepts, and\n" +
	"those a router image adds, by API group, and the fields of the inventory and the lab file that\n" +
	"chalkctl and chalklab read. An image whose modules declare kinds of their own under\n" +
	"[`chalkos.kinds`](extension-api.md#chalkoskinds) accepts those as well.\n\n" +
	"Every document has an `apiVersion`, a `kind`, a `metadata.name` of lower-case letters, digits,\n" +
	"`-` and `.`, and a `spec`, whose fields the tables list. A field within another is listed by its\n" +
	"whole path, where `<name>` stands for each key of a map and `[]` for each item of a list:\n" +
	"`spec.vip.addresses` is the field `addresses` of the object `spec.vip`. An object has a row of\n" +
	"its own only when it has a description, may be null or must be set; an object a document leaves\n" +
	"out takes the defaults of its fields. A default that reads \"required\" means the field must be\n" +
	"set, and `—` means it has no default. A field the tables do not list is refused, except within a\n" +
	"Kubernetes object or an object whose type reads \"object, any fields\".\n"

// kindsSources closes the page of config kinds.
const kindsSources = "\nSources: the JSON Schemas in the `schemas` directory of an image\n" +
	"[build](image-layout.md#outputs-of-an-image-build) of the default variant and of a router\n" +
	"image, from `modules/node/kinds/builtin/`, `modules/node/kubernetes/kinds.nix`, the\n" +
	"`kinds.nix` files of `modules/node/router/` and the tables of the image's systemd-networkd, and\n" +
	"`pkg/inventory/chalkctl.chalkos.dev_v1alpha1_Inventory.json` and\n" +
	"`pkg/chalklab/chalklab.chalkos.dev_v1alpha1_Lab.json`.\n"

// groupIntros open the sections of chalkos's API groups.
var groupIntros = map[string]string{
	"chalkos.dev":            "The kinds of `chalkos.dev` set the node's host name and hold the secrets that other documents name.",
	"kubernetes.chalkos.dev": "The kinds of `kubernetes.chalkos.dev` set up the Kubernetes cluster and the node's part in it. Only images that run Kubernetes accept them, and Flannel only those whose CNI is flannel.",
	"networkd.chalkos.dev":   "The kinds of `networkd.chalkos.dev` are the .link, .netdev and .network files of the image's systemd as documents. Their schemas are generated from that systemd, so they accept the sections and keys it reads.",
	"oci.chalkos.dev":        "The kinds of `oci.chalkos.dev` set how the node reaches OCI registries: the credentials of each host and the mirrors of a registry.",
	"router.chalkos.dev":     "The kinds of `router.chalkos.dev` set how a router forwards and filters: its forwarding and reverse-path filter, its firewall's zones, policies and address sets, and its NAT rules. Only router images accept them.",
	"storage.chalkos.dev":    "The kinds of `storage.chalkos.dev` lay out the node's disks: the system disk and the volumes.",
	"time.chalkos.dev":       "The kinds of `time.chalkos.dev` set how the node synchronises its clock.",
}

// filesHeading and filesIntro open the section of the files given to writeKinds.
const (
	filesHeading = "Inventory and lab files"
	filesAnchor  = "inventory-and-lab-files"
	filesIntro   = "chalkctl reads an inventory and chalklab a lab file. Neither is part of a node's config."
)

// kindSchema is a kind, the compiled schemas that declare it, and how many change rules it has.
type kindSchema struct {
	path string
	// decl is the kind as its schema declares it.
	decl           schema.Kind
	group, version string
	// file tells that the kind is one of chalkctl's or chalklab's own files rather than a
	// document of a node's config.
	file bool
	// description is the root's, the sentence that opens the kind's section.
	description string
	schemas     *schema.Schemas
	// rules is the number of rules the kind has, as rules.Compile compiles them.
	rules int
}

// row is a field in a kind's table.
type row struct {
	path, typ, def, desc string
}

// writeKinds writes the page of config kinds to page: a section per API group of the kinds whose
// schemas are in dir, an image's schemas directory, chalkos.dev's first and the others by name,
// with the kinds of each by name, then a section of the kinds of the schema files given,
// chalkctl's and chalklab's own, in their order.
func writeKinds(page, dir string, files []string) error {
	s, err := schema.LoadSchemas(dir)
	if err != nil {
		return err
	}
	kinds, err := kindSchemas(s, dir)
	if err != nil {
		return err
	}
	slices.SortFunc(kinds, func(a, b kindSchema) int {
		if (a.group == "chalkos.dev") != (b.group == "chalkos.dev") {
			if a.group == "chalkos.dev" {
				return -1
			}
			return 1
		}
		return cmp.Or(strings.Compare(a.group, b.group), strings.Compare(a.decl.Kind, b.decl.Kind))
	})
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		s, err := schema.LoadFiles(map[string][]byte{filepath.Base(f): data})
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		ks, err := kindSchemas(s, filepath.Dir(f))
		if err != nil {
			return err
		}
		for _, k := range ks {
			k.file = true
			kinds = append(kinds, k)
		}
	}
	anchors := map[string]bool{filesAnchor: true}
	for _, k := range kinds {
		anchors[groupAnchor(k.group)] = true
	}
	for _, k := range kinds {
		anchor := strings.ToLower(k.decl.Kind)
		if anchors[anchor] {
			return fmt.Errorf("two kinds named %s would share the anchor #%s", k.decl.Kind, anchor)
		}
		anchors[anchor] = true
	}
	for _, k := range kinds {
		if k.description == "" {
			return fmt.Errorf("%s: the kind %s has no description, which its section opens with", k.path, k.decl.Kind)
		}
	}
	var b strings.Builder
	b.WriteString(frontMatter("Config kinds", kindsDescription))
	b.WriteString(kindsIntro)
	group, inFiles := "", false
	for _, k := range kinds {
		switch {
		case k.file && !inFiles:
			inFiles = true
			fmt.Fprintf(&b, "\n## %s { #%s }\n\n%s\n", filesHeading, filesAnchor, filesIntro)
		case !k.file && k.group != group:
			group = k.group
			intro, ok := groupIntros[group]
			if !ok {
				intro = fmt.Sprintf("The kinds of the API group `%s`.", group)
			}
			fmt.Fprintf(&b, "\n## %s { #%s }\n\n%s\n", group, groupAnchor(group), intro)
		}
		writeKind(&b, k)
	}
	b.WriteString(kindsSources)
	return os.WriteFile(page, []byte(b.String()), 0o644)
}

// groupAnchor is the anchor of an API group's section: its name with hyphens for its dots, which
// no kind's anchor can be, as kind names have neither.
func groupAnchor(group string) string { return strings.ReplaceAll(group, ".", "-") }

// kindSchemas are the kinds s declares, those of the schema files in dir, each with the number of
// rules the evaluator compiles for it.
func kindSchemas(s *schema.Schemas, dir string) ([]kindSchema, error) {
	set, err := rules.Compile(s)
	if err != nil {
		return nil, err
	}
	var kinds []kindSchema
	for _, k := range s.Kinds() {
		ks := kindSchema{decl: k, schemas: s, path: filepath.Join(dir, schemaFileName(k))}
		ks.group, ks.version, _ = strings.Cut(k.APIVersion, "/")
		s.WalkSchema(k.APIVersion, k.Kind, func(n schema.Node) bool {
			ks.description = text(schema.KeywordsOf(n.Schema)["description"])
			return false
		})
		for _, p := range set.Programs {
			if p.Kind == k {
				ks.rules++
			}
		}
		kinds = append(kinds, ks)
	}
	return kinds, nil
}

// schemaFileName is the name of k's schema file, as pkg/schema requires it.
func schemaFileName(k schema.Kind) string {
	group, version, _ := strings.Cut(k.APIVersion, "/")
	return group + "_" + version + "_" + k.Kind + ".json"
}

// writeKind writes a kind's section: its purpose, its apiVersion, how many documents of it a
// node's config may hold, how many change rules it has, and its fields, or a networkd kind's
// sections.
func writeKind(b *strings.Builder, k kindSchema) {
	fmt.Fprintf(b, "\n### %s { #%s }\n\n%s\n\n`apiVersion: %s/%s`\n\n",
		k.decl.Kind, strings.ToLower(k.decl.Kind), k.description, k.group, k.version)
	var counts []string
	if !k.file {
		if k.decl.Multiple {
			counts = append(counts, "A node's config may hold several, each with a name of its own.")
		} else {
			counts = append(counts, "A node's config holds at most one.")
		}
		if k.decl.Withheld {
			counts = append(counts, "The kind is [withheld](../how-it-works/node/config.md#withheld-kinds-stay-out-of-run): no service reads its documents, and chalkcfg prints none.")
		}
	}
	// The page of built-ins lists the rules of each kind under #rules-<kind>.
	if k.rules == 1 {
		counts = append(counts, fmt.Sprintf("It has a [change rule](builtins.md#rules-%s).", strings.ToLower(k.decl.Kind)))
	} else if k.rules > 1 {
		counts = append(counts, fmt.Sprintf("It has %d [change rules](builtins.md#rules-%s).", k.rules, strings.ToLower(k.decl.Kind)))
	}
	s := walkSpec(k)
	// A kind's own declarations mark its spec, each a tuple of the spec's fields.
	for _, d := range s.unique {
		counts = append(counts, "No two documents share "+keyFields(d)+".")
	}
	if len(counts) > 0 {
		b.WriteString(strings.Join(counts, " ") + "\n\n")
	}
	if len(s.sections) > 0 {
		writeSections(b, s)
		return
	}
	if len(s.rows) == 0 {
		b.WriteString("Its `spec` has no fields.\n")
		return
	}
	b.WriteString("| Field | Type | Default | Description |\n| --- | --- | --- | --- |\n")
	for _, r := range s.rows {
		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n", r.path, r.typ, r.def, r.desc)
	}
}

// spec is what a kind's section shows of its spec: the spec's own uniqueness declarations, a row
// for each field, and, for a networkd kind, whose fields are the sections of its unit, the
// systemd spelling of each section and of each section's keys.
type spec struct {
	unique   []schema.Unique
	rows     []row
	sections []section
}

// section is a networkd section: its field of the spec, its systemd spelling, and its keys, each
// with its spelling, by field.
type section struct {
	field, name string
	keys        map[string]string
}

// walkSpec walks the spec of k's schema. A field has a row of its own, in the order of the walk,
// which is by path, and the fields within it follow it: an object's fields, the fields of a
// map's values at <name> and those of a list's items at []. An object that only groups its
// fields has their rows alone; the fields of a secret reference or a Kubernetes object have none.
func walkSpec(k kindSchema) spec {
	var s spec
	// required are the required fields of each object, by its path, which an anyOf and each of
	// its branches share.
	required := map[string]map[string]bool{}
	seen := map[string]bool{}
	sections := map[string]*section{}
	k.schemas.WalkSchema(k.decl.APIVersion, k.decl.Kind, func(n schema.Node) bool {
		switch {
		case len(n.Field) == 0:
			return true
		case n.Field[0] != "spec":
			return false
		}
		kw, a := n.Keywords(), n.Annotations()
		for _, r := range asSlice(kw["required"]) {
			if name, ok := r.(string); ok {
				if required[n.Path] == nil {
					required[n.Path] = map[string]bool{}
				}
				required[n.Path][name] = true
			}
		}
		if len(n.Field) == 1 {
			s.unique = append(s.unique, a.Unique...)
			return true
		}
		if a.NetworkdSection != "" && len(n.Field) == 2 {
			if sections[n.Field[1]] == nil {
				sections[n.Field[1]] = &section{field: n.Field[1], name: a.NetworkdSection, keys: map[string]string{}}
			}
			return true
		}
		if sec := sections[n.Field[1]]; sec != nil {
			if a.NetworkdKey != "" {
				sec.keys[n.Field[len(n.Field)-1]] = a.NetworkdKey
				return false
			}
			return true
		}
		last := n.Field[len(n.Field)-1]
		if last != schema.ItemStep && last != schema.ValueStep && !seen[n.Path] {
			seen[n.Path] = true
			if r, ok := fieldRow(k, n, required[schema.FieldPath(n.Field[:len(n.Field)-1])][last]); ok {
				s.rows = append(s.rows, r)
			}
		}
		return !(a.Ref != nil && a.Ref.Key) && !a.KubernetesObject
	})
	for _, name := range slices.Sorted(maps.Keys(sections)) {
		s.sections = append(s.sections, *sections[name])
	}
	return s
}

// fieldRow is the row of the field n, or none for an object that only groups its fields: one that
// may be left out and has no description of its own would add nothing to the rows of its fields.
func fieldRow(k kindSchema, n schema.Node, required bool) (row, bool) {
	kw := n.Keywords()
	def := defaultOf(kw, required)
	desc := text(kw["description"])
	if _, isObject := kw["properties"]; isObject && def == "—" && desc == "" {
		return row{}, false
	}
	if max, ok := kw["maxItems"]; ok {
		desc = strings.TrimSpace(desc + " Holds " + display(max) + " items at most.")
	}
	if note := uniqueNote(k.decl.Kind, kw, n.Schema); note != "" {
		desc = strings.TrimSpace(desc + " " + note)
	}
	return row{path: n.Path, typ: tableCell(typeOf(kw, n.Schema)), def: tableCell(def), desc: desc}, true
}

// typeOf describes the values a schema accepts: sch, $ref followed, of the keywords s.
func typeOf(s map[string]any, sch *jsonschema.Schema) string {
	a := schema.AnnotationsOf(sch)
	switch {
	case a.Ref != nil && a.Ref.Key:
		return "secret reference"
	case a.Ref != nil:
		return refType(a.Ref)
	case a.KubernetesObject:
		return "Kubernetes object"
	case s["const"] != nil:
		return "`" + display(s["const"]) + "`"
	case len(asSlice(s["enum"])) == 1:
		return "`" + display(asSlice(s["enum"])[0]) + "`"
	case s["enum"] != nil:
		var values []string
		for _, v := range asSlice(s["enum"]) {
			values = append(values, "`"+display(v)+"`")
		}
		return "one of " + strings.Join(values, ", ")
	case len(sch.AnyOf) > 0:
		var types []string
		nullable := false
		for _, branch := range sch.AnyOf {
			if schema.KeywordsOf(branch)["type"] == "null" {
				nullable = true
				continue
			}
			types = append(types, typeOf(schema.KeywordsOf(branch), schema.Resolve(branch)))
		}
		if nullable {
			types = append(types, "null")
		}
		return strings.Join(types, " or ")
	}
	if ts, ok := s["type"].([]any); ok {
		var names []string
		for _, t := range ts {
			if name, ok := t.(string); ok {
				names = append(names, name)
			}
		}
		return strings.Join(names, " or ")
	}
	switch t, _ := s["type"].(string); t {
	case "integer":
		return "integer" + rangeOf(s)
	case "array":
		if sch.Items2020 == nil {
			return "list of any"
		}
		return "list of " + typeOf(schema.KeywordsOf(sch.Items2020), schema.Resolve(sch.Items2020))
	case "object":
		if v, ok := sch.AdditionalProperties.(*jsonschema.Schema); ok {
			return "map of " + typeOf(schema.KeywordsOf(v), schema.Resolve(v))
		}
		if s["additionalProperties"] != false {
			return "object, any fields"
		}
		return "object"
	case "string":
		if format, _ := s["format"].(string); formatNames[format] != "" {
			return formatNames[format]
		}
		return t
	case "boolean", "number", "null":
		return t
	}
	return "any"
}

// formatNames name the strings of the formats the kinds' schemas assert, as pkg/schema checks
// them.
var formatNames = map[string]string{
	"ip":                        "IP address",
	"cidr":                      "address range",
	"port-range":                "range of ports",
	"image-reference":           "image reference",
	"versioned-image-reference": "image reference with a version tag",
}

// rangeOf describes the bounds of an integer schema.
func rangeOf(s map[string]any) string {
	lo, hasLo := s["minimum"]
	hi, hasHi := s["maximum"]
	switch {
	case hasLo && hasHi:
		return fmt.Sprintf(" from %s to %s", display(lo), display(hi))
	case hasLo:
		return " of at least " + display(lo)
	case hasHi:
		return " of at most " + display(hi)
	}
	return ""
}

// defaultOf is a field's default as JSON, "required" for a required field without one, or "—".
func defaultOf(s map[string]any, required bool) string {
	if d, ok := s["default"]; ok {
		data, _ := json.Marshal(d)
		return "`" + string(data) + "`"
	}
	if required {
		return "required"
	}
	return "—"
}

// display is a value as a table shows it: a string as it is, anything else as JSON.
func display(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	data, _ := json.Marshal(v)
	return string(data)
}

// writeSections writes the sections of a networkd kind: each field of its spec is a section of
// the unit, whose keys are networkd's in camelCase, with the spelling networkd reads.
func writeSections(b *strings.Builder, s spec) {
	b.WriteString("Each field of `spec` is a section of the unit: an object, or a list of objects for a\n" +
		"section written more than once. Each key of a section is networkd's key in camelCase and takes\n" +
		"a value, or a list for a key written more than once. The table gives each section and key as\n" +
		"networkd spells it.\n\n")
	b.WriteString("| Field | networkd section | Keys |\n| --- | --- | --- |\n")
	for _, sec := range s.sections {
		var keys []string
		for _, key := range slices.Sorted(maps.Keys(sec.keys)) {
			keys = append(keys, fmt.Sprintf("`%s` (%s)", key, sec.keys[key]))
		}
		fmt.Fprintf(b, "| `spec.%s` | %s | %s |\n", sec.field, sec.name, strings.Join(keys, ", "))
	}
}

// text is a description as one line of a table cell.
func text(v any) string {
	s, _ := v.(string)
	return tableCell(strings.Join(strings.Fields(s), " "))
}

// normaliserText is how a field's values compare under each normaliser.
var normaliserText = map[string]string{
	"ip":    "as addresses",
	"cidr":  "as masked ranges",
	"host":  "as registry hosts",
	"lower": "in lower case",
}

// uniqueNote says what a field's x-chalkos-unique declares, a sentence for each declaration: the
// values or list items that must not repeat, among all documents of the kind or within one list,
// and the fields of the key and how they compare.
func uniqueNote(kind string, kw map[string]any, sch *jsonschema.Schema) string {
	t := kw
	for _, branch := range sch.AnyOf {
		if b := schema.KeywordsOf(branch); b["type"] != "null" {
			t = b
		}
	}
	var notes []string
	for _, d := range schema.AnnotationsOf(sch).Unique {
		note := "Unique"
		if t["type"] == "array" {
			note = "Each item is unique"
		}
		if d.Across == "list" {
			note += " within the list"
		} else {
			note += " among the " + kind + " documents"
		}
		if d.By != nil {
			note += " by " + keyFields(d)
		} else if d.As != "" {
			note += ", compared " + normaliserText[d.As]
		}
		notes = append(notes, note+".")
	}
	return strings.Join(notes, " ")
}

// keyFields lists the fields of a declaration's key, each with how it compares.
func keyFields(d schema.Unique) string {
	var fields []string
	for _, f := range d.By {
		text := "`" + f.Field + "`"
		if f.As != "" {
			text += " " + normaliserText[f.As]
		}
		fields = append(fields, text)
	}
	switch len(fields) {
	case 0:
		return ""
	case 1:
		return fields[0]
	}
	return strings.Join(fields[:len(fields)-1], ", ") + " and " + fields[len(fields)-1]
}

// refType describes a reference to a document: the kind it names, after an before a vowel as
// AddressSet takes it, and the names it reserves, which the field takes as they are.
func refType(ref *schema.Ref) string {
	kind := ref.Kind
	// The first letter alone picks the article, which suits the kinds there are; a kind spoken
	// otherwise, such as one starting with a U said as "you", would take the wrong one.
	article := "a "
	if kind != "" && strings.ContainsRune("AEIOU", rune(kind[0])) {
		article = "an "
	}
	out := "name of " + article + kind
	var reserved []string
	for _, n := range ref.Reserved {
		reserved = append(reserved, "`"+n+"`")
	}
	if len(reserved) > 0 {
		out += ", or " + strings.Join(reserved, ", ")
	}
	return out
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
