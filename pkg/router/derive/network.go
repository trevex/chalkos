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

// networkDocs are the Network documents of byKind with their specs, in their order.
func networkDocs(byKind map[string]any) ([]Named[map[string]any], error) {
	docs, _ := byKind["Network"].([]any)
	out := make([]Named[map[string]any], 0, len(docs))
	for _, d := range docs {
		doc, _ := d.(map[string]any)
		name, err := DocumentName("Network", doc)
		if err != nil {
			return nil, err
		}
		spec, _ := doc["spec"].(map[string]any)
		out = append(out, Named[map[string]any]{name, spec})
	}
	return out, nil
}

// networkLinks are the interface names in the [Match] sections' Name= of the Network documents,
// sorted and once each, as matchLinks reads them.
func networkLinks(networks []Named[map[string]any]) []string {
	seen := map[string]bool{}
	for _, n := range networks {
		for _, l := range matchLinks(n.Spec) {
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

// networkAddresses are the values of Address= in the [Network] and [Address] sections and of
// ServerAddress= in the [DHCPServer] section of the Network documents that parseAddrOrPrefix
// reads; others, such as an empty value, give none.
func networkAddresses(networks []Named[map[string]any]) []NetworkAddress {
	var out []NetworkAddress
	for _, n := range networks {
		for _, key := range []struct{ section, key string }{{"network", "address"}, {"address", "address"}, {"dhcpServer", "serverAddress"}} {
			for _, s := range OneOrMany(n.Spec[key.section]) {
				section, _ := s.(map[string]any)
				for _, v := range OneOrMany(section[key.key]) {
					if p, ok := parseAddrOrPrefix(v); ok {
						out = append(out, NetworkAddress{Network: n.Name, Prefix: p})
					}
				}
			}
		}
	}
	return out
}

// parseAddrOrPrefix reads a value of a unit that is an address, with or without a prefix length,
// once the whitespace around it goes; an address alone is a prefix of its full length.
func parseAddrOrPrefix(v any) (netip.Prefix, bool) {
	text, _ := v.(string)
	text = strings.Trim(text, UnitWhitespace)
	if p, err := netip.ParsePrefix(text); err == nil {
		return p, true
	}
	a, err := netip.ParseAddr(text)
	if err != nil {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(a, a.BitLen()), true
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
