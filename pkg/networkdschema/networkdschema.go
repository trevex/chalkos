// Package networkdschema generates the spec schemas of chalkos's networkd kinds from systemd's
// gperf tables, the sections and keys networkd accepts. Documents spell names in camelCase; each
// schema records the systemd spelling, from which chalkos-render renders the unit files.
package networkdschema

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// SectionKeyword and KeyKeyword hold the systemd names of a section and a key in a kind's schema:
// SectionKeyword on the spec field of a section and on the section's definition, KeyKeyword on
// the section's field of a key.
const (
	SectionKeyword = "x-chalkos-networkd-section"
	KeyKeyword     = "x-chalkos-networkd-key"
)

// Entry is one Section.Key a gperf table accepts.
type Entry struct {
	Section string
	Key     string
}

var (
	entryLine   = regexp.MustCompile(`^([A-Za-z0-9-]+)\.([A-Za-z0-9-]+),`)
	commentLine = regexp.MustCompile(`^\s*/\*.*\*/\s*$`)
)

// Parse reads the entries of a gperf table: the lines after its first %% line that start with
// Section.Key. Any other body line must be blank or a /* … */ comment, so a change to gperf's own
// table syntax is never mistaken for an empty table.
func Parse(r io.Reader) ([]Entry, error) {
	s := bufio.NewScanner(r)
	body := false
	lineNo := 0
	var entries []Entry
	for s.Scan() {
		lineNo++
		line := s.Text()
		if strings.HasPrefix(line, "%%") {
			if body {
				break
			}
			body = true
			continue
		}
		if !body {
			continue
		}
		switch {
		case entryLine.MatchString(line):
			m := entryLine.FindStringSubmatch(line)
			entries = append(entries, Entry{Section: m[1], Key: m[2]})
		case strings.TrimSpace(line) == "", commentLine.MatchString(line):
			// Blank and comment lines carry no entry.
		default:
			return nil, fmt.Errorf("line %d: neither a Section.Key entry, a comment nor blank: %q", lineNo, line)
		}
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, errors.New("no Section.Key entries in the table")
	}
	return entries, nil
}

// CamelCase spells a networkd name as documents do: dashes removed and the leading word or
// acronym in lower case, a version suffix included, so MACAddress becomes macAddress, SR-IOV
// sriov and IPv6AcceptRA ipv6AcceptRA.
func CamelCase(name string) string {
	return lowerLeading(strings.ReplaceAll(name, "-", ""))
}

// mixedCasePrefixes are leading tokens that mix case on their own, so the general rule of
// lowering all but the last capital of the leading run would cut them wrong.
var mixedCasePrefixes = []string{"MACsec", "IPoIB"}

func lowerLeading(word string) string {
	for _, prefix := range mixedCasePrefixes {
		if strings.HasPrefix(word, prefix) {
			return strings.ToLower(prefix) + word[len(prefix):]
		}
	}

	r := []rune(word)
	n := 0
	for n < len(r) && (unicode.IsUpper(r[n]) || unicode.IsDigit(r[n])) {
		n++
	}
	lower := n - 1
	switch {
	case n == len(r):
		// The whole word is the acronym: DHCP, LLDP, L2TP.
		lower = n
	case n <= 1:
		// A single leading capital starts an ordinary word: Name, Network.
		lower = 1
	case r[n] == 'v' && n+1 < len(r) && unicode.IsDigit(r[n+1]):
		// IPv4, IPv6, DHCPv4 and DHCPv6 are themselves lower case, version digit included.
		lower = n
	}
	for i := 0; i < lower; i++ {
		r[i] = unicode.ToLower(r[i])
	}
	return string(r)
}

// scalarSchema is a key's value, which networkd parses from text. A string spans no lines, since
// a line break would end the key in the unit; each call returns a fresh map, so keys share none.
func scalarSchema() map[string]any {
	return map[string]any{"type": []any{"string", "integer", "boolean"}, "pattern": "^[^\r\n]*$"}
}

func oneOrMany(item map[string]any) map[string]any {
	return map[string]any{"anyOf": []any{item, map[string]any{"type": "array", "items": item}}}
}

// SpecSchema returns {"spec": …, "$defs": …}: the schema of a kind's spec, with one property
// per section, and one definition per section under $defs. A section is an object or, repeated
// as networkd allows for [Address], [Route] and others, a list of objects.
func SpecSchema(entries []Entry) (map[string]any, error) {
	keys := map[string]map[string]bool{}
	for _, e := range entries {
		if keys[e.Section] == nil {
			keys[e.Section] = map[string]bool{}
		}
		keys[e.Section][e.Key] = true
	}
	defs := map[string]any{}
	specProps := map[string]any{}
	sectionOf := map[string]string{}
	for _, section := range slices.Sorted(maps.Keys(keys)) {
		yamlSection := CamelCase(section)
		if other, ok := sectionOf[yamlSection]; ok {
			return nil, fmt.Errorf("sections %s and %s are both %s in YAML", other, section, yamlSection)
		}
		sectionOf[yamlSection] = section

		props := map[string]any{}
		keyOf := map[string]string{}
		for _, key := range slices.Sorted(maps.Keys(keys[section])) {
			yamlKey := CamelCase(key)
			if other, ok := keyOf[yamlKey]; ok {
				return nil, fmt.Errorf("section %s: keys %s and %s are both %s in YAML", section, other, key, yamlKey)
			}
			keyOf[yamlKey] = key
			prop := oneOrMany(scalarSchema())
			prop[KeyKeyword] = key
			props[yamlKey] = prop
		}
		defs[section] = map[string]any{
			"type":                 "object",
			"properties":           props,
			"additionalProperties": false,
			SectionKeyword:         section,
		}
		sectionProp := oneOrMany(map[string]any{"$ref": "#/$defs/" + section})
		// The section name also sits next to this property's own anyOf, the way a key's
		// x-chalkos-networkd-key sits next to its anyOf, so a renderer reads the systemd section
		// name from the property itself.
		sectionProp[SectionKeyword] = section
		specProps[yamlSection] = sectionProp
	}
	return map[string]any{
		"spec": map[string]any{
			"type":                 "object",
			"properties":           specProps,
			"additionalProperties": false,
		},
		"$defs": defs,
	}, nil
}

// Section is a networkd section as a kind's schema spells it: its systemd name and each of its
// keys' systemd names, by the key's field.
type Section struct {
	Name string
	Keys map[string]string
}

// Names reads the systemd names of the sections and keys of a networkd kind's schema, by each
// section's field of the spec. It reads only the layout SpecSchema writes, with the standard
// library alone, for chalkos-render.
func Names(kindSchema map[string]any) map[string]Section {
	spec, _ := field(kindSchema, "properties", "spec", "properties").(map[string]any)
	names := map[string]Section{}
	for name, prop := range spec {
		section, _ := field(prop, SectionKeyword).(string)
		if section == "" {
			continue
		}
		keys := map[string]string{}
		props, _ := field(kindSchema, "$defs", section, "properties").(map[string]any)
		for key, p := range props {
			if spelling, _ := field(p, KeyKeyword).(string); spelling != "" {
				keys[key] = spelling
			}
		}
		names[name] = Section{Name: section, Keys: keys}
	}
	return names
}

// field is the value at the keys below v, nil where an object lacks one.
func field(v any, keys ...string) any {
	for _, k := range keys {
		m, _ := v.(map[string]any)
		v = m[k]
	}
	return v
}
