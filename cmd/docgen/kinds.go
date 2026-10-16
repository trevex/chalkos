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
	"This page lists the fields of the config kinds that an image of `chalkos.lib.mkImages`'s\n" +
	"default variant accepts, and of the inventory and the lab file that chalkctl and chalklab read.\n" +
	"It is generated from their JSON Schemas: those the image carries in the `schemas` directory of\n" +
	"its [build](image-layout.md#outputs-of-an-image-build), and those chalkctl and chalklab are\n" +
	"built with. An image whose modules declare kinds of their own under\n" +
	"[`chalkos.kinds`](options.md#chalkoskinds) accepts those as well, and its `schemas` directory\n" +
	"holds their schemas.\n\n" +
	"Every document has an `apiVersion`, a `kind`, a `metadata.name` of lower-case letters, digits,\n" +
	"`-` and `.`, and a `spec`, whose fields the tables list. A field whose default reads \"required\"\n" +
	"must be set. A field the tables do not list is refused, except within a Kubernetes object or an\n" +
	"object whose type reads \"any fields\".\n"

// kindSchema is the JSON Schema of a kind, as its file holds it, and the kind it declares.
type kindSchema struct {
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

// writeKinds writes the page of config kinds to page: the kinds whose schemas are in dir, an
// image's schemas directory, those of chalkos.dev first and the others by group and kind, then
// the kinds of the schema files given, chalkctl's and chalklab's own, in their order.
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
	anchors := map[string]bool{}
	for _, k := range kinds {
		anchor := strings.ToLower(k.kind)
		if anchors[anchor] {
			return fmt.Errorf("two kinds named %s would share the anchor #%s", k.kind, anchor)
		}
		anchors[anchor] = true
	}
	var b strings.Builder
	b.WriteString(frontMatter("Config kinds", kindsDescription))
	b.WriteString(kindsIntro)
	for _, k := range kinds {
		writeKind(&b, k)
	}
	return os.WriteFile(page, []byte(b.String()), 0o644)
}

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
	k := kindSchema{root: root}
	k.group, _ = xk["group"].(string)
	k.version, _ = xk["version"].(string)
	k.kind, _ = xk["kind"].(string)
	k.multiple, _ = xk["multiple"].(bool)
	if k.group == "" || k.version == "" || k.kind == "" {
		return kindSchema{}, fmt.Errorf("%s: has no x-chalkos-kind with a group, version and kind", path)
	}
	return k, nil
}

// writeKind writes a kind's section: its apiVersion, how many documents of it a node's config may
// hold, and its fields, or a networkd kind's sections.
func writeKind(b *strings.Builder, k kindSchema) {
	fmt.Fprintf(b, "\n## %s { #%s }\n\n`apiVersion: %s/%s`\n\n", k.kind, strings.ToLower(k.kind), k.group, k.version)
	if d := text(k.root["description"]); d != "" {
		b.WriteString(d + "\n\n")
	}
	if !k.file {
		if k.multiple {
			b.WriteString("A node's config may hold several, each with a name of its own.\n\n")
		} else {
			b.WriteString("A node's config holds at most one.\n\n")
		}
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

// fields returns a row for each field of the object schema s, whose path is path, in name order,
// each followed by the rows of the fields within it.
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
		// An object whose fields follow has no default of its own: its fields' defaults apply.
		if def == "none" && len(inner) > 0 {
			def = ""
		}
		rows = append(rows, row{
			path: fieldPath,
			typ:  cell(typeOf(root, child)),
			def:  cell(def),
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

// defaultOf is a field's default as JSON, "required" for a required field without one, or "none".
func defaultOf(s map[string]any, required bool) string {
	if d, ok := s["default"]; ok {
		data, _ := json.Marshal(d)
		return "`" + string(data) + "`"
	}
	if required {
		return "required"
	}
	return "none"
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
	return cell(strings.Join(strings.Fields(s), " "))
}

// cell escapes the pipes of a table cell's text.
func cell(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

func props(s map[string]any) map[string]any { return asMap(s["properties"]) }

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
