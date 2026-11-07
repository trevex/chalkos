// Package identity holds a node's identity: the form its config translates to, which the node
// applies at boot (identity.json in each image's config slot below /state/config, then
// /run/chalkos/node.json) and its Kubernetes side reads. It imports nothing of chalkos but the
// storage section, so every package on the node may use it.
package identity

import (
	"maps"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/storage"
)

// Identity is the node's identity without secrets, as the node applies it.
type Identity struct {
	Hostname string `json:"hostname"`
	// Platform is the platform the node's image is built for, which its config is translated for.
	Platform string            `json:"platform"`
	Network  map[string]any    `json:"network"`
	Labels   map[string]string `json:"labels"`
	Taints   []Taint           `json:"taints"`
	// Storage is how the node partitions, encrypts and mounts its disks.
	Storage    storage.Section `json:"storage"`
	Kubernetes *Kubernetes     `json:"kubernetes"`
}

// Kubernetes is what the node is in Kubernetes; nil on nodes of an image without Kubernetes.
type Kubernetes struct {
	// NodeName is the node's name in the cluster, which its Node object and kubelet
	// certificates carry.
	NodeName string `json:"nodeName"`
	// NodeIPs are the node's fixed addresses, at most one per address family; the node picks
	// the addresses of the other families at boot.
	NodeIPs []string `json:"nodeIPs"`
	// ValidSubnets are the subnets the node picks its addresses from instead of its cluster's;
	// nil uses the cluster's.
	ValidSubnets []string `json:"validSubnets"`
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

// Taint is a Kubernetes taint applied to the node.
type Taint struct {
	Key string `json:"key"`
	// Value is empty for a taint without a value.
	Value  string `json:"value"`
	Effect string `json:"effect"`
}
