// Package identity holds a node's identity: the form its config translates to, which the node
// applies at boot (identity.json in each image's config slot below /state/config, then
// /run/chalkos/node.json) and its layers read. Every image gives the node its hostname, platform,
// network and storage; each layer of the image keeps its own part below Layers, by the layer's
// name, which this package does not interpret. It imports nothing of chalkos but the storage
// section, so every package on the node may use it.
package identity

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/storage"
)

// Identity is the node's identity without secrets, as the node applies it.
type Identity struct {
	Hostname string `json:"hostname"`
	// Platform is the platform the node's image is built for, which its config is translated for.
	Platform string         `json:"platform"`
	Network  map[string]any `json:"network"`
	// Storage is how the node partitions, encrypts and mounts its disks.
	Storage storage.Section `json:"storage"`
	// Layers are the parts of the identity the image's layers add, by layer name; a layer that
	// adds nothing has no entry.
	Layers map[string]json.RawMessage `json:"layers"`
}

// SetLayer stores v as the part of the layer called name, replacing what the layer had.
func (id *Identity) SetLayer(name string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("the identity's %s part: %w", name, err)
	}
	if id.Layers == nil {
		id.Layers = map[string]json.RawMessage{}
	}
	id.Layers[name] = data
	return nil
}

// Layer decodes the part of the layer called name into v and tells whether the identity has
// one; v stays as it is when it has none.
func (id Identity) Layer(name string, v any) (bool, error) {
	data, ok := id.Layers[name]
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return true, fmt.Errorf("the identity's %s part: %w", name, err)
	}
	return true, nil
}

// StaticAddresses returns the addresses of the node's networkd networks, in network name order,
// without prefix lengths.
func (id Identity) StaticAddresses() []string {
	networks, _ := id.Network["networks"].(map[string]any)
	var addrs []string
	for _, name := range slices.Sorted(maps.Keys(networks)) {
		network, _ := networks[name].(map[string]any)
		list, _ := network["address"].([]any)
		for _, a := range list {
			if s, ok := a.(string); ok {
				addr, _, _ := strings.Cut(s, "/")
				addrs = append(addrs, addr)
			}
		}
	}
	return addrs
}
