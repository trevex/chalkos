package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	"image, from `modules/node/kinds/builtin/` and the tables of the image's systemd-networkd, and\n" +
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

// kindSchema is the JSON Schema of a kind, as its file holds it, and the kind it declares.
type kindSchema struct {
	path                 string
	group, version, kind string
	multiple             bool
	// file tells that the kind is one of chalkctl's or chalklab's own files rather than a
	// document of a node's config.
	file bool
	root map[string]any
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
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var kinds []kindSchema
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		k, err := readKindSchema(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		kinds = append(kinds, k)
	}
	slices.SortFunc(kinds, func(a, b kindSchema) int {
		if (a.group == "chalkos.dev") != (b.group == "chalkos.dev") {
			if a.group == "chalkos.dev" {
				return -1
			}
			return 1
		}
		return cmp.Or(strings.Compare(a.group, b.group), strings.Compare(a.kind, b.kind))
	})
	for _, f := range files {
		k, err := readKindSchema(f)
		if err != nil {
			return err
		}
		k.file = true
		kinds = append(kinds, k)
	}
	anchors := map[string]bool{filesAnchor: true}
	for _, k := range kinds {
		anchors[groupAnchor(k.group)] = true
	}
	for _, k := range kinds {
		anchor := strings.ToLower(k.kind)
		if anchors[anchor] {
			return fmt.Errorf("two kinds named %s would share the anchor #%s", k.kind, anchor)
		}
		anchors[anchor] = true
	}
	for _, k := range kinds {
		if text(k.root["description"]) == "" {
			return fmt.Errorf("%s: the kind %s has no description, which its section opens with", k.path, k.kind)
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

// readKindSchema reads a kind's JSON Schema, keeping numbers as their text, and the kind its
// "x-chalkos-kind" declares.
func readKindSchema(path string) (kindSchema, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return kindSchema{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return kindSchema{}, fmt.Errorf("%s: %w", path, err)
	}
	xk := asMap(root["x-chalkos-kind"])
	k := kindSchema{path: path, root: root}
	k.group, _ = xk["group"].(string)
	k.version, _ = xk["version"].(string)
	k.kind, _ = xk["kind"].(string)
	k.multiple, _ = xk["multiple"].(bool)
	if k.group == "" || k.version == "" || k.kind == "" {
		return kindSchema{}, fmt.Errorf("%s: has no x-chalkos-kind with a group, version and kind", path)
	}
	return k, nil
}

// writeKind writes a kind's section: its purpose, its apiVersion, how many documents of it a
// node's config may hold, how many change rules it has, and its fields, or a networkd kind's
// sections.
func writeKind(b *strings.Builder, k kindSchema) {
	fmt.Fprintf(b, "\n### %s { #%s }\n\n%s\n\n`apiVersion: %s/%s`\n\n",
		k.kind, strings.ToLower(k.kind), text(k.root["description"]), k.group, k.version)
	var counts []string
	if !k.file {
		if k.multiple {
			counts = append(counts, "A node's config may hold several, each with a name of its own.")
		} else {
			counts = append(counts, "A node's config holds at most one.")
		}
	}
	// The page of built-ins lists the rules of each kind under #rules-<kind>.
	if n := countRules(k.root); n == 1 {
		counts = append(counts, fmt.Sprintf("It has a [change rule](builtins.md#rules-%s).", strings.ToLower(k.kind)))
	} else if n > 1 {
		counts = append(counts, fmt.Sprintf("It has %d [change rules](builtins.md#rules-%s).", n, strings.ToLower(k.kind)))
	}
	if len(counts) > 0 {
		b.WriteString(strings.Join(counts, " ") + "\n\n")
	}
	spec := resolve(k.root, asMap(props(k.root)["spec"]))
	if isNetworkd(spec) {
		writeSections(b, k.root, spec)
		return
	}
	rows := fields(k.root, "spec", spec)
	if len(rows) == 0 {
		b.WriteString("Its `spec` has no fields.\n")
		return
	}
	b.WriteString("| Field | Type | Default | Description |\n| --- | --- | --- | --- |\n")
	for _, r := range rows {
		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n", r.path, r.typ, r.def, r.desc)
	}
}

// countRules counts the change rules of a kind's schema: its own, in x-chalkos-kind, and its
// fields', in their x-chalkos-rules.
func countRules(v any) int {
	n := 0
	switch v := v.(type) {
	case map[string]any:
		for key, val := range v {
			switch key {
			case "x-chalkos-rules":
				n += len(asSlice(val))
			case "x-chalkos-kind":
				n += len(asSlice(asMap(val)["rules"]))
			default:
				n += countRules(val)
			}
		}
	case []any:
		for _, val := range v {
			n += countRules(val)
		}
	}
	return n
}

// fields returns a row for each field of the object schema s, whose path is path, in name order,
// each followed by the rows of the fields within it; an object that only groups its fields has
// their rows alone.
func fields(root map[string]any, path string, s map[string]any) []row {
	s = resolve(root, s)
	required := map[string]bool{}
	for _, r := range asSlice(s["required"]) {
		if name, ok := r.(string); ok {
			required[name] = true
		}
	}
	p := props(s)
	var rows []row
	for _, name := range slices.Sorted(maps.Keys(p)) {
		child := resolve(root, asMap(p[name]))
		fieldPath := path + "." + name
		inner := nested(root, fieldPath, child)
		def := defaultOf(child, required[name])
		// An object that only groups its fields, which may be left out and has no description of
		// its own, would add nothing to the rows of its fields.
		if _, isObject := child["properties"]; isObject && def == "—" && text(child["description"]) == "" {
			rows = append(rows, inner...)
			continue
		}
		rows = append(rows, row{
			path: fieldPath,
			typ:  tableCell(typeOf(root, child)),
			def:  tableCell(def),
			desc: text(child["description"]),
		})
		rows = append(rows, inner...)
	}
	return rows
}

// nested returns the rows of the fields within a field: an object's fields, the fields of a map's
// values at <name> and those of a list's items at [].
func nested(root map[string]any, path string, s map[string]any) []row {
	s = resolve(root, s)
	if s["x-chalkos-secret-ref"] == true || s["x-chalkos-kubernetes-object"] == true {
		return nil
	}
	if _, ok := s["properties"]; ok {
		return fields(root, path, s)
	}
	if v, ok := s["additionalProperties"].(map[string]any); ok {
		return nested(root, path+".<name>", v)
	}
	if items, ok := s["items"].(map[string]any); ok {
		return nested(root, path+"[]", items)
	}
	for _, branch := range asSlice(s["anyOf"]) {
		if rows := nested(root, path, asMap(branch)); len(rows) > 0 {
			return rows
		}
	}
	return nil
}

// typeOf describes the values a schema accepts.
func typeOf(root, s map[string]any) string {
	s = resolve(root, s)
	switch {
	case s["x-chalkos-secret-ref"] == true:
		return "secret reference"
	case s["x-chalkos-kubernetes-object"] == true:
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
	case s["anyOf"] != nil:
		var types []string
		nullable := false
		for _, branch := range asSlice(s["anyOf"]) {
			if asMap(branch)["type"] == "null" {
				nullable = true
				continue
			}
			types = append(types, typeOf(root, asMap(branch)))
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
		return "list of " + typeOf(root, asMap(s["items"]))
	case "object":
		if v, ok := s["additionalProperties"].(map[string]any); ok {
			return "map of " + typeOf(root, v)
		}
		if s["additionalProperties"] != false {
			return "object, any fields"
		}
		return "object"
	case "string", "boolean", "number", "null":
		return t
	}
	return "any"
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

// resolve returns the schema that a "$ref" to the root's "$defs" names, with the other keywords of
// the referring schema, such as its description, laid over it.
func resolve(root, s map[string]any) map[string]any {
	ref, ok := s["$ref"].(string)
	if !ok {
		return s
	}
	merged := maps.Clone(asMap(asMap(root["$defs"])[strings.TrimPrefix(ref, "#/$defs/")]))
	if merged == nil {
		merged = map[string]any{}
	}
	for k, v := range s {
		if k != "$ref" {
			merged[k] = v
		}
	}
	return merged
}

// isNetworkd tells whether a spec is a networkd unit's, whose fields are its sections.
func isNetworkd(spec map[string]any) bool {
	for _, v := range props(spec) {
		if _, ok := asMap(v)["x-chalkos-networkd-section"]; ok {
			return true
		}
	}
	return false
}

// writeSections writes the sections of a networkd kind: each field of its spec is a section of
// the unit, whose keys are networkd's in camelCase, with the spelling networkd reads.
func writeSections(b *strings.Builder, root, spec map[string]any) {
	b.WriteString("Each field of `spec` is a section of the unit: an object, or a list of objects for a\n" +
		"section written more than once. Each key of a section is networkd's key in camelCase and takes\n" +
		"a value, or a list for a key written more than once. The table gives each section and key as\n" +
		"networkd spells it.\n\n")
	b.WriteString("| Field | networkd section | Keys |\n| --- | --- | --- |\n")
	p := props(spec)
	for _, name := range slices.Sorted(maps.Keys(p)) {
		section := asMap(p[name])
		kp := props(sectionObject(root, section))
		var keys []string
		for _, key := range slices.Sorted(maps.Keys(kp)) {
			spelling, _ := asMap(kp[key])["x-chalkos-networkd-key"].(string)
			keys = append(keys, fmt.Sprintf("`%s` (%s)", key, spelling))
		}
		spelling, _ := section["x-chalkos-networkd-section"].(string)
		fmt.Fprintf(b, "| `spec.%s` | %s | %s |\n", name, spelling, strings.Join(keys, ", "))
	}
}

// sectionObject returns the object schema of a networkd section, which a document gives alone or
// in a list.
func sectionObject(root, section map[string]any) map[string]any {
	for _, branch := range append([]any{section}, asSlice(section["anyOf"])...) {
		if s := resolve(root, asMap(branch)); s["properties"] != nil {
			return s
		}
	}
	return nil
}

// text is a description as one line of a table cell.
func text(v any) string {
	s, _ := v.(string)
	return tableCell(strings.Join(strings.Fields(s), " "))
}

func props(s map[string]any) map[string]any { return asMap(s["properties"]) }

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
