// Package manifest decodes the cluster manifest that a chalkos cluster definition generates
// (`chalkos.<cluster>.manifest`). The format is unstable while SchemaVersion is 0.
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/trevex/chalkos/pkg/storage"
)

// SchemaVersion is the manifest version this package understands.
const SchemaVersion = 0

// Kinds of Kubernetes nodes a role's nodes are.
const (
	KindControlPlane = "controlplane"
	KindWorker       = "worker"
)

// Manifest describes a cluster: its API endpoint, the role images, and every node.
type Manifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	Cluster       Cluster         `json:"cluster"`
	Roles         map[string]Role `json:"roles"`
	Nodes         map[string]Node `json:"nodes"`
}

// Cluster identifies the cluster and its Kubernetes API server.
type Cluster struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
}

// Role is a node role; all nodes of a role run the same image.
type Role struct {
	// Image is the attribute path, relative to the cluster's attribute, that builds the role's
	// unsigned disk image; the CLI prepends the attribute path it evaluated the manifest from.
	Image string `json:"image"`
	// Kind is what the role's nodes are in Kubernetes: KindControlPlane, KindWorker, or empty.
	Kind string `json:"kind"`
}

// Node is one machine of the cluster, by the role it runs and the values unique to it.
type Node struct {
	Role     string   `json:"role"`
	Identity Identity `json:"identity"`
}

// Identity is the node's identity without secrets, as delivered to the node.
type Identity struct {
	Hostname string         `json:"hostname"`
	Network  map[string]any `json:"network"`
	// NetworkUnits are the systemd-networkd unit files rendered from Network, by file name.
	NetworkUnits map[string]string `json:"networkUnits"`
	Labels       map[string]string `json:"labels"`
	Taints       []Taint           `json:"taints"`
	// Storage is how the node partitions, encrypts and mounts its disks.
	Storage    storage.Section            `json:"storage"`
	Kubernetes *KubernetesIdentity        `json:"kubernetes"`
	Extensions map[string]json.RawMessage `json:"extensions"`
}

// KubernetesIdentity is what the node is in Kubernetes; nil on nodes of a role without
// Kubernetes.
type KubernetesIdentity struct {
	// NodeName is the node's name in the cluster, which its Node object and kubelet
	// certificates carry.
	NodeName string `json:"nodeName"`
	// NodeIP is the node's fixed address; empty picks one on the node at boot.
	NodeIP string `json:"nodeIP"`
	// ValidSubnets are the subnets the node picks its address from instead of its cluster's;
	// nil uses the cluster's.
	ValidSubnets []string `json:"validSubnets"`
}

// StaticAddresses returns the addresses of the node's networkd networks, in network name order,
// without prefix lengths.
func (id Identity) StaticAddresses() []string {
	networks, _ := id.Network["networks"].(map[string]any)
	names := make([]string, 0, len(networks))
	for name := range networks {
		names = append(names, name)
	}
	sort.Strings(names)
	var addrs []string
	for _, name := range names {
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

// Decode reads a manifest and rejects versions and fields this package does not know, so a
// cluster definition newer than the binary fails loudly instead of being half understood.
func Decode(r io.Reader) (*Manifest, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var header struct {
		SchemaVersion *int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	switch v := header.SchemaVersion; {
	case v == nil:
		return nil, errors.New("manifest has no schemaVersion")
	case *v > SchemaVersion:
		return nil, fmt.Errorf("manifest schemaVersion %d is not supported (want %d): chalkctl is older than the cluster definition", *v, SchemaVersion)
	case *v < SchemaVersion:
		return nil, fmt.Errorf("manifest schemaVersion %d is not supported (want %d): the cluster definition uses an older manifest format; update chalkos", *v, SchemaVersion)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	// Disk references decode themselves and keep unknown selector keys instead of refusing them.
	for name, n := range m.Nodes {
		for disk, d := range n.Identity.Storage.Disks {
			if keys := d.Ref.UnknownKeys(); len(keys) > 0 {
				return nil, fmt.Errorf("parse manifest: node %s, disk %s: unknown selector keys %s", name, disk, strings.Join(keys, ", "))
			}
		}
	}
	return &m, nil
}
