// Package manifest decodes the cluster manifest that a chalkos cluster definition generates
// (`chalkos.<cluster>.manifest`). The format is unstable while SchemaVersion is 0. chalkctl and
// chalklab read it; nothing on a node does.
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
)

// SchemaVersion is the manifest version this package understands.
const SchemaVersion = 0

// Manifest describes a cluster: its API endpoint, the role images, and every node.
type Manifest struct {
	SchemaVersion int        `json:"schemaVersion"`
	Cluster       Cluster    `json:"cluster"`
	SecureBoot    SecureBoot `json:"secureBoot"`
	// Kubernetes and Flannel are the specs of the Kubernetes and Flannel documents of every node
	// with Kubernetes, as the cluster definition renders them; Flannel is null without flannel.
	// They are carried as they are, and checked against an image's schemas once they are
	// documents.
	Kubernetes json.RawMessage   `json:"kubernetes"`
	Flannel    json.RawMessage   `json:"flannel"`
	Registries Registries        `json:"registries"`
	Manifests  []json.RawMessage `json:"manifests"`
	Time       ClusterTime       `json:"time"`
	Roles      map[string]Role   `json:"roles"`
	Nodes      map[string]Node   `json:"nodes"`
}

// Cluster identifies the cluster and its Kubernetes API server.
type Cluster struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
}

// SecureBoot is how the cluster's images are signed.
type SecureBoot struct {
	// SignerCertificate is the PEM certificate of the db key that signs the images' UKIs; empty
	// when the cluster definition names none.
	SignerCertificate string `json:"signerCertificate"`
}

// Registries are the mirrors of container registries, by registry host.
type Registries struct {
	Mirrors        map[string][]string `json:"mirrors"`
	AllowPlainHTTP bool                `json:"allowPlainHTTP"`
}

// ClusterTime is how every node keeps its clock beyond its servers.
type ClusterTime struct {
	// DHCPServers adds the time servers DHCP announces.
	DHCPServers bool `json:"dhcpServers"`
}

// IsNull tells whether a raw section is absent or null.
func IsNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// Role is a node role: all nodes of a role on a platform run the same image.
type Role struct {
	// Images are the attribute paths, relative to the cluster's attribute, that build the role's
	// unsigned disk image for each platform; the CLI prepends the attribute path it evaluated the
	// manifest from.
	Images map[string]string `json:"images"`
}

// Node is one machine of the cluster, by the role it runs, the platform it runs on and the values
// unique to it.
type Node struct {
	Role     string `json:"role"`
	Platform string `json:"platform"`
	// Kind is what the node is in Kubernetes: identity.KindControlPlane, identity.KindWorker, or
	// empty on a role without Kubernetes.
	Kind     string            `json:"kind"`
	Identity identity.Identity `json:"identity"`
	// Documents are the config documents the node's extensions add, each one JSON object.
	Documents []json.RawMessage `json:"documents"`
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
		if err := n.Identity.Time.Check(); err != nil {
			return nil, fmt.Errorf("parse manifest: node %s: %w", name, err)
		}
		// The node would refuse them at every boot and run no kubelet.
		if k := n.Identity.Kubernetes; k != nil {
			if _, err := nodeip.ParseFilter(k.ValidSubnets); err != nil {
				return nil, fmt.Errorf("parse manifest: node %s: kubernetes.%w", name, err)
			}
			if err := k.CheckNodeIPs(); err != nil {
				return nil, fmt.Errorf("parse manifest: node %s: kubernetes.%w", name, err)
			}
		}
	}
	return &m, nil
}
