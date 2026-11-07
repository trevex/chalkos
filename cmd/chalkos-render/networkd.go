package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/router/derive"
)

// unitSuffix is the file suffix of each networkd kind's units, in the order they are rendered.
var unitSuffix = []struct{ kind, suffix string }{{"Link", ".link"}, {"NetDev", ".netdev"}, {"Network", ".network"}}

// renderNetworkd writes each Link, NetDev and Network document of in as a networkd unit file,
// named as the document with its kind's suffix. Section and key names come from the
// x-chalkos-networkd-section and x-chalkos-networkd-key annotations of the kinds' schemas in the
// directory schemas, the image's, so a unit says on the node exactly what networkd's own names
// would. [Match] comes first and other sections follow by name; keys are in name order, and a
// repeated section or key keeps the order of its list.
func renderNetworkd(schemas string, in map[string]any, out string) error {
	byKind := documents(in)
	for _, k := range unitSuffix {
		docs, _ := byKind[k.kind].([]any)
		if len(docs) == 0 {
			continue
		}
		raw, err := readSchema(filepath.Join(schemas, "networkd.chalkos.dev_v1alpha1_"+k.kind+".json"))
		if err != nil {
			return err
		}
		for _, d := range docs {
			doc, _ := d.(map[string]any)
			name, err := derive.DocumentName(k.kind, doc)
			if err != nil {
				return err
			}
			unit, err := renderUnit(k.kind+" "+name, doc, raw)
			if err != nil {
				return err
			}
			if err := writeFile(out, name+k.suffix, []byte(unit)); err != nil {
				return err
			}
		}
	}
	return nil
}

func readSchema(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the image's networkd schema: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("the image's networkd schema %s: %w", path, err)
	}
	return raw, nil
}

func renderUnit(what string, doc, raw map[string]any) (string, error) {
	specProps := dig(raw, "properties", "spec", "properties")
	defs, _ := raw["$defs"].(map[string]any)
	spec, _ := doc["spec"].(map[string]any)
	type section struct {
		name string
		body map[string]any
		def  map[string]any
	}
	var sections []section
	for yamlName, v := range spec {
		prop, _ := specProps[yamlName].(map[string]any)
		name, _ := prop["x-chalkos-networkd-section"].(string)
		// The schema refuses a spec property that lacks this annotation; a document that bypassed
		// it must not render as an empty systemd section name.
		if name == "" {
			return "", derive.InternalErrorf("%s: spec.%s has no x-chalkos-networkd-section annotation", what, yamlName)
		}
		def := sectionDef(defs, prop)
		for _, body := range derive.OneOrMany(v) {
			m, _ := body.(map[string]any)
			sections = append(sections, section{name: name, body: m, def: def})
		}
	}
	slices.SortStableFunc(sections, func(x, y section) int {
		a, b := x.name, y.name
		if (a == "Match") != (b == "Match") {
			if a == "Match" {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	var b strings.Builder
	for _, sec := range sections {
		keyProps := dig(sec.def, "properties")
		lines := make([][2]string, 0, len(sec.body))
		for yamlKey, v := range sec.body {
			prop, _ := keyProps[yamlKey].(map[string]any)
			key, _ := prop["x-chalkos-networkd-key"].(string)
			// As for the section name: a key with no annotation fails rather than render as an
			// empty systemd key.
			if key == "" {
				return "", derive.InternalErrorf("%s: %s.%s has no x-chalkos-networkd-key annotation", what, sec.name, yamlKey)
			}
			for _, v := range derive.OneOrMany(v) {
				value, err := derive.UnitValue(v)
				if err != nil {
					return "", derive.Within(fmt.Sprintf("%s: %s.%s", what, sec.name, key), err)
				}
				lines = append(lines, [2]string{key, value})
			}
		}
		slices.SortStableFunc(lines, func(a, b [2]string) int { return strings.Compare(a[0], b[0]) })
		fmt.Fprintf(&b, "[%s]\n", sec.name)
		for _, l := range lines {
			fmt.Fprintf(&b, "%s=%s\n", l[0], l[1])
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// sectionDef is the definition a spec property refers to, by its own $ref or by that of a branch
// of its anyOf: one section, or a list of them. It is nil when the property refers to none.
func sectionDef(defs, prop map[string]any) map[string]any {
	candidates := []map[string]any{prop}
	branches, _ := prop["anyOf"].([]any)
	for _, b := range branches {
		branch, _ := b.(map[string]any)
		candidates = append(candidates, branch, dig(branch, "items"))
	}
	for _, c := range candidates {
		ref, _ := c["$ref"].(string)
		if name, ok := strings.CutPrefix(ref, "#/$defs/"); ok {
			def, _ := defs[name].(map[string]any)
			return def
		}
	}
	return nil
}

func dig(m map[string]any, path ...string) map[string]any {
	for _, p := range path {
		next, _ := m[p].(map[string]any)
		m = next
	}
	return m
}
