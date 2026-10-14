// Package networkdschema generates the spec schemas of chalkos's networkd kinds from systemd's
// gperf tables, the lists of sections and keys networkd itself accepts in .network, .netdev and
// .link files. Section and key names are camelCase in documents; each schema records the systemd
// spelling, from which chalkd renders the unit files.
package networkdschema

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Entry is one Section.Key a gperf table accepts.
type Entry struct {
	Section string
	Key     string
}

var entryLine = regexp.MustCompile(`^([A-Za-z0-9-]+)\.([A-Za-z0-9-]+),`)

// Parse reads the entries of a gperf table: the lines after its first %% line that start with
// Section.Key.
func Parse(r io.Reader) ([]Entry, error) {
	s := bufio.NewScanner(r)
	body := false
	var entries []Entry
	for s.Scan() {
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
		if m := entryLine.FindStringSubmatch(line); m != nil {
			entries = append(entries, Entry{Section: m[1], Key: m[2]})
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

// CamelCase spells a networkd name as documents do: its leading word or acronym in lower case,
// the rest unchanged, so MACAddress becomes macAddress. An acronym followed by a one-letter
// lower-case part, or by "sec", is lowered whole, so IPv6AcceptRA becomes ipv6AcceptRA, IPoIB
// ipoIB and MACsec macsec. A dash separates words: SR-IOV becomes srIOV.
func CamelCase(name string) string {
	parts := strings.Split(name, "-")
	parts[0] = lowerLeading(parts[0])
	return strings.Join(parts, "")
}

func lowerLeading(word string) string {
	r := []rune(word)
	n := 0
	for n < len(r) && (unicode.IsUpper(r[n]) || unicode.IsDigit(r[n])) {
		n++
	}
	lower := n - 1
	switch {
	case n == len(r):
		lower = n
	case n <= 1:
		lower = 1
	default:
		m := n
		for m < len(r) && unicode.IsLower(r[m]) {
			m++
		}
		if seg := string(r[n:m]); len(seg) == 1 || seg == "sec" {
			lower = n
		}
	}
	for i := 0; i < lower; i++ {
		r[i] = unicode.ToLower(r[i])
	}
	return string(r)
}

// A key's value: networkd parses every value from text, and a key given as a list is written
// once per element.
var scalar = map[string]any{"type": []any{"string", "integer", "boolean"}}

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
	sections := make([]string, 0, len(keys))
	for s := range keys {
		sections = append(sections, s)
	}
	sort.Strings(sections)

	defs := map[string]any{}
	specProps := map[string]any{}
	sectionOf := map[string]string{}
	for _, section := range sections {
		yamlSection := CamelCase(section)
		if other, ok := sectionOf[yamlSection]; ok {
			return nil, fmt.Errorf("sections %s and %s are both %s in YAML", other, section, yamlSection)
		}
		sectionOf[yamlSection] = section

		props := map[string]any{}
		keyOf := map[string]string{}
		names := make([]string, 0, len(keys[section]))
		for k := range keys[section] {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, key := range names {
			yamlKey := CamelCase(key)
			if other, ok := keyOf[yamlKey]; ok {
				return nil, fmt.Errorf("section %s: keys %s and %s are both %s in YAML", section, other, key, yamlKey)
			}
			keyOf[yamlKey] = key
			prop := oneOrMany(scalar)
			prop["x-chalkos-networkd-key"] = key
			props[yamlKey] = prop
		}
		defs[section] = map[string]any{
			"type":                       "object",
			"properties":                 props,
			"additionalProperties":       false,
			"x-chalkos-networkd-section": section,
		}
		specProps[yamlSection] = oneOrMany(map[string]any{"$ref": "#/$defs/" + section})
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
