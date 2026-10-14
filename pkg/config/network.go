package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/trevex/chalkos/pkg/schema"
)

// unitSuffix is the file suffix of each networkd kind's units.
var unitSuffix = map[string]string{"Network": ".network", "NetDev": ".netdev", "Link": ".link"}

// renderNetworkUnits renders the Network, NetDev and Link documents into networkd unit files by
// file name. Section and key names come from the schemas' x-chalkos-networkd-section and
// x-chalkos-networkd-key, so a unit says on the node exactly what networkd's own names would.
// [Match] comes first and other sections follow by name; keys are in name order, and a repeated
// section or key keeps the order of its list.
func renderNetworkUnits(docs []Document, s *schema.Schemas) (map[string]string, error) {
	units := map[string]string{}
	for _, d := range docs {
		suffix, ok := unitSuffix[d.Kind]
		if !ok || d.APIVersion != NetworkdAPI {
			continue
		}
		raw, ok := s.Raw(d.APIVersion, d.Kind)
		if !ok {
			return nil, fmt.Errorf("%s: the image has no schema of %s", d, d.Kind)
		}
		unit, err := renderUnit(d, raw.(map[string]any))
		if err != nil {
			return nil, err
		}
		units[d.Name+suffix] = unit
	}
	return units, nil
}

func renderUnit(d Document, raw map[string]any) (string, error) {
	specProps := dig(raw, "properties", "spec", "properties")
	defs, _ := raw["$defs"].(map[string]any)
	spec, _ := d.Object["spec"].(map[string]any)
	type section struct {
		name string
		body map[string]any
		def  map[string]any
	}
	var sections []section
	for yamlName, v := range spec {
		prop, _ := specProps[yamlName].(map[string]any)
		name, _ := prop["x-chalkos-networkd-section"].(string)
		// The schema rejects a spec property that lacks this annotation, but a document that
		// bypassed validation must not render as an empty systemd section name.
		if name == "" {
			return "", fmt.Errorf("%s: spec.%s has no x-chalkos-networkd-section annotation", d, yamlName)
		}
		def, _ := defs[name].(map[string]any)
		for _, body := range oneOrMany(v) {
			m, _ := body.(map[string]any)
			sections = append(sections, section{name: name, body: m, def: def})
		}
	}
	sort.SliceStable(sections, func(i, j int) bool {
		a, b := sections[i].name, sections[j].name
		if (a == "Match") != (b == "Match") {
			return a == "Match"
		}
		return a < b
	})
	var b strings.Builder
	for _, sec := range sections {
		keyProps := dig(sec.def, "properties")
		names := make([]string, 0, len(sec.body))
		for yamlKey := range sec.body {
			names = append(names, yamlKey)
		}
		lines := make([][2]string, 0, len(names))
		for _, yamlKey := range names {
			prop, _ := keyProps[yamlKey].(map[string]any)
			key, _ := prop["x-chalkos-networkd-key"].(string)
			// Same defensiveness as the section name above: a key with no annotation must fail
			// rather than render as an empty systemd key.
			if key == "" {
				return "", fmt.Errorf("%s: %s.%s has no x-chalkos-networkd-key annotation", d, sec.name, yamlKey)
			}
			for _, v := range oneOrMany(sec.body[yamlKey]) {
				value, err := unitValue(v)
				if err != nil {
					return "", fmt.Errorf("%s: %s.%s: %w", d, sec.name, key, err)
				}
				lines = append(lines, [2]string{key, value})
			}
		}
		sort.SliceStable(lines, func(i, j int) bool { return lines[i][0] < lines[j][0] })
		fmt.Fprintf(&b, "[%s]\n", sec.name)
		for _, l := range lines {
			fmt.Fprintf(&b, "%s=%s\n", l[0], l[1])
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func unitValue(v any) (string, error) {
	switch v := v.(type) {
	case string:
		if strings.ContainsAny(v, "\n\r") {
			return "", fmt.Errorf("a value cannot span lines")
		}
		return v, nil
	case bool:
		if v {
			return "yes", nil
		}
		return "no", nil
	case json.Number:
		return v.String(), nil
	default:
		return "", fmt.Errorf("%v is not a string, number or boolean", v)
	}
}

func oneOrMany(v any) []any {
	if list, ok := v.([]any); ok {
		return list
	}
	return []any{v}
}

func dig(m map[string]any, path ...string) map[string]any {
	for _, p := range path {
		next, _ := m[p].(map[string]any)
		m = next
	}
	return m
}

// networkMap is what Go reads of the node's network besides its units, in the shape the
// cluster definition delivered: each Network document's addresses, from [Network]'s Address= and
// [Address] sections, and the MAC address its [Match] names.
func networkMap(docs []Document) map[string]any {
	networks := map[string]any{}
	for _, d := range docs {
		if d.APIVersion != NetworkdAPI || d.Kind != "Network" {
			continue
		}
		spec, _ := d.Object["spec"].(map[string]any)
		var addrs []any
		for _, n := range oneOrMany(spec["network"]) {
			if m, ok := n.(map[string]any); ok {
				addrs = append(addrs, oneOrManyNonNil(m["address"])...)
			}
		}
		for _, a := range oneOrMany(spec["address"]) {
			if m, ok := a.(map[string]any); ok {
				addrs = append(addrs, oneOrManyNonNil(m["address"])...)
			}
		}
		entry := map[string]any{"address": addrs}
		for _, m := range oneOrMany(spec["match"]) {
			if mm, ok := m.(map[string]any); ok && mm["macAddress"] != nil {
				entry["matchConfig"] = map[string]any{"MACAddress": mm["macAddress"]}
			}
		}
		networks[d.Name] = entry
	}
	return map[string]any{"networks": networks}
}

func oneOrManyNonNil(v any) []any {
	if v == nil {
		return nil
	}
	return oneOrMany(v)
}
