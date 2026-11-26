package config

import (
	"fmt"
	"strings"

	"github.com/trevex/chalkos/pkg/identity"
)

// oneOrMany is v as a list: a section or a value of networkd's documents is one, or a list of them.
func oneOrMany(v any) []any {
	if list, ok := v.([]any); ok {
		return list
	}
	return []any{v}
}

// NetworkMap is what Go reads of the node's network: each Network document's [Network] Address=
// values, the only addresses that count as static, so a node's default addresses and certificate
// names stay what they were; and the MAC addresses its [Match] names, joined by spaces as
// networkd writes them.
func NetworkMap(docs []Document) map[string]any {
	networks := map[string]any{}
	for _, d := range docs {
		if d.APIVersion != NetworkdAPI || d.Kind != "Network" {
			continue
		}
		spec, _ := d.Object["spec"].(map[string]any)
		entry := map[string]any{"address": identity.NetworkAddresses(spec)}
		for _, m := range oneOrMany(spec["match"]) {
			if mm, ok := m.(map[string]any); ok && mm["macAddress"] != nil {
				entry["matchConfig"] = map[string]any{"MACAddress": joinValues(mm["macAddress"])}
			}
		}
		networks[d.Name] = entry
	}
	return map[string]any{"networks": networks}
}

// joinValues joins a list of strings with spaces and returns any other value unchanged.
func joinValues(v any) any {
	list, ok := v.([]any)
	if !ok {
		return v
	}
	parts := make([]string, 0, len(list))
	for _, item := range list {
		parts = append(parts, fmt.Sprint(item))
	}
	return strings.Join(parts, " ")
}

// StaticAddresses returns the addresses Set.Identity counts as the node's static ones, in network
// name order and without prefix lengths, read from the documents alone, so a client finds a node
// before it has the schemas of the node's image.
func StaticAddresses(docs []Document) []string {
	return identity.Identity{Network: NetworkMap(docs)}.StaticAddresses()
}

// Hostname returns the Host document's spec.hostname, read from the documents alone: empty
// without a Host or for a null one, which take the node's name.
func Hostname(docs []Document) string {
	var hostname string
	for _, d := range docs {
		if d.APIVersion == CoreAPI && d.Kind == "Host" {
			host, _ := d.Object["spec"].(map[string]any)
			hostname, _ = host["hostname"].(string)
		}
	}
	return hostname
}
