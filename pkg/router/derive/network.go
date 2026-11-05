package derive

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The Network documents as the model reads them: the links their [Match] sections name, their
// static addresses and their specs, with the helpers that read the values of a networkd document
// as systemd does.

// networkLinks are the interface names in the [Match] sections' Name= of the Network documents
// of byKind, sorted and once each, as matchLinks reads them.
func networkLinks(byKind map[string]any) []string {
	docs, _ := byKind["Network"].([]any)
	seen := map[string]bool{}
	for _, d := range docs {
		doc, _ := d.(map[string]any)
		spec, _ := doc["spec"].(map[string]any)
		for _, l := range matchLinks(spec) {
			seen[l] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// NetworkAddress is a static address that the Network document Network gives its links.
type NetworkAddress struct {
	Network string
	Prefix  netip.Prefix
}

// networkAddresses are the addresses of Address= in the [Network] and [Address] sections and of
// ServerAddress= in the [DHCPServer] section of the Network documents of byKind that parse as an
// address, with or without a prefix length, once the spaces around them go; others, such as an
// empty value, give none.
func networkAddresses(byKind map[string]any) []NetworkAddress {
	var out []NetworkAddress
	docs, _ := byKind["Network"].([]any)
	for _, d := range docs {
		doc, _ := d.(map[string]any)
		name, err := DocumentName("Network", doc)
		if err != nil {
			continue
		}
		spec, _ := doc["spec"].(map[string]any)
		for _, key := range []struct{ section, key string }{{"network", "address"}, {"address", "address"}, {"dhcpServer", "serverAddress"}} {
			for _, s := range OneOrMany(spec[key.section]) {
				section, _ := s.(map[string]any)
				for _, v := range OneOrMany(section[key.key]) {
					text, _ := v.(string)
					text = strings.TrimSpace(text)
					p, err := netip.ParsePrefix(text)
					if err != nil {
						a, err := netip.ParseAddr(text)
						if err != nil {
							continue
						}
						p = netip.PrefixFrom(a, a.BitLen())
					}
					out = append(out, NetworkAddress{Network: name, Prefix: p})
				}
			}
		}
	}
	return out
}

// networkSpecs are the specs of the Network documents of byKind, by name.
func networkSpecs(byKind map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	docs, _ := byKind["Network"].([]any)
	for _, d := range docs {
		doc, _ := d.(map[string]any)
		name, err := DocumentName("Network", doc)
		if err != nil {
			continue
		}
		spec, _ := doc["spec"].(map[string]any)
		out[name] = spec
	}
	return out
}

// OneOrMany are the values of a networkd document's section or key, which holds one value or a
// list of them.
func OneOrMany(v any) []any {
	if list, ok := v.([]any); ok {
		return list
	}
	return []any{v}
}

// ItemPath is the path of the i-th value OneOrMany reads from v, the field at base: base itself
// for a single value, base[i] for an item of a list.
func ItemPath(base string, v any, i int) string {
	if _, ok := v.([]any); ok {
		return fmt.Sprintf("%s[%d]", base, i)
	}
	return base
}

// UnitValue is v as a unit file writes it. The networkd schemas take a string without line
// breaks, an integer or a boolean alone; a line break would end the key and let the rest set keys
// or sections of its own, so it stays a guard.
func UnitValue(v any) (string, error) {
	switch v := v.(type) {
	case string:
		if strings.ContainsAny(v, "\n\r") {
			return "", InternalErrorf("a value cannot span lines")
		}
		return v, nil
	case bool:
		// As NixOS's systemd-lib toOption renders a bool.
		return strconv.FormatBool(v), nil
	case json.Number:
		return v.String(), nil
	default:
		return "", InternalErrorf("%v is not a string, number or boolean", v)
	}
}

// UnitWhitespace are the characters systemd splits a value at, and strips around it.
const UnitWhitespace = " \t\n\r"

// InterfaceName is an interface name as the schemas take it, or a prefix of one with the one
// trailing * that nftables matches.
var InterfaceName = regexp.MustCompile(`^(?:[A-Za-z0-9_.:@-]{1,15}|[A-Za-z0-9_.:@-]{0,14}[*])$`)

// PlainInterface is an interface's name without a glob.
var PlainInterface = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,15}$`)
