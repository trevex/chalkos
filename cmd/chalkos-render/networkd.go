package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/networkdschema"
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
		names, err := readNames(schemas, k.kind)
		if err != nil {
			return err
		}
		for _, d := range docs {
			doc, _ := d.(map[string]any)
			name, err := derive.DocumentName(k.kind, doc)
			if err != nil {
				return err
			}
			unit, err := renderUnit(k.kind+" "+name, doc, names)
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

// readNames reads the systemd names of the sections and keys of the networkd kind's schema in the
// directory dir, by their fields.
func readNames(dir, kind string) (map[string]networkdschema.Section, error) {
	path := filepath.Join(dir, "networkd.chalkos.dev_v1alpha1_"+kind+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the image's networkd schema: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("the image's networkd schema %s: %w", path, err)
	}
	return networkdschema.Names(raw), nil
}

func renderUnit(what string, doc map[string]any, names map[string]networkdschema.Section) (string, error) {
	spec, _ := doc["spec"].(map[string]any)
	type section struct {
		name string
		keys map[string]string
		body map[string]any
	}
	var sections []section
	for yamlName, v := range spec {
		name := names[yamlName].Name
		// The schema refuses a spec property that lacks this annotation; a document that bypassed
		// it must not render as an empty systemd section name.
		if name == "" {
			return "", derive.InternalErrorf("%s: spec.%s has no x-chalkos-networkd-section annotation", what, yamlName)
		}
		for _, body := range derive.OneOrMany(v) {
			m, _ := body.(map[string]any)
			sections = append(sections, section{name: name, keys: names[yamlName].Keys, body: m})
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
		lines := make([][2]string, 0, len(sec.body))
		for yamlKey, v := range sec.body {
			key := sec.keys[yamlKey]
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
