// Package networkdschema generates the spec schemas of chalkos's networkd kinds from systemd's
// gperf tables, the lists of sections and keys networkd itself accepts in .network, .netdev and
// .link files. Section and key names are camelCase in documents; each schema records the systemd
// spelling, from which chalkos-render renders the unit files.
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

// CamelCase spells a networkd name as documents do: its leading word or acronym in lower case,
// the rest unchanged, so MACAddress becomes macAddress and VLANId becomes vlanId. A dash is
// removed before lowering, so SR-IOV becomes sriov and SR-IOVVirtualFunctions becomes
// sriovVirtualFunctions. A version digit glues itself to the acronym before it, so IPv6AcceptRA
// becomes ipv6AcceptRA and DHCPv4 becomes dhcpv4. IPoIB and MACsec are themselves the leading
// token and lower whole regardless of what follows: IPoIB becomes ipoib and
// MACsecReceiveAssociation becomes macsecReceiveAssociation.
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

// scalarSchema is a key's value: networkd parses every value from text, and a key given as a
// list is written once per element. A string spans no lines, because a line break would end the
// key and start another in the unit. It returns a fresh map each call, since callers embed it in
// their own schema and must not share one map between keys.
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
			prop := oneOrMany(scalarSchema())
			prop["x-chalkos-networkd-key"] = key
			props[yamlKey] = prop
		}
		defs[section] = map[string]any{
			"type":                       "object",
			"properties":                 props,
			"additionalProperties":       false,
			"x-chalkos-networkd-section": section,
		}
		sectionProp := oneOrMany(map[string]any{"$ref": "#/$defs/" + section})
		// The section name also sits next to this property's own anyOf, the way a key's
		// x-chalkos-networkd-key sits next to its anyOf, so a renderer reads the systemd section
		// name from the property itself.
		sectionProp["x-chalkos-networkd-section"] = section
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
