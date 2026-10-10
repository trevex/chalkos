// Package manifest decodes the cluster manifest that a chalkos cluster definition generates
// (`chalkos.<cluster>.manifest`). The format is unstable while SchemaVersion is 0.
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
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
	SecureBoot    SecureBoot      `json:"secureBoot"`
	Roles         map[string]Role `json:"roles"`
	Nodes         map[string]Node `json:"nodes"`
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

// Role is a node role; all nodes of a role on a platform run the same image.
type Role struct {
	// Images are the attribute paths, relative to the cluster's attribute, that build the role's
	// unsigned disk image for each platform; the CLI prepends the attribute path it evaluated the
	// manifest from.
	Images map[string]string `json:"images"`
	// Kind is what the role's nodes are in Kubernetes: KindControlPlane, KindWorker, or empty.
	Kind string `json:"kind"`
}

// Node is one machine of the cluster, by the role it runs, the platform it runs on and the values
// unique to it.
type Node struct {
	Role     string   `json:"role"`
	Platform string   `json:"platform"`
	Identity Identity `json:"identity"`
}

// Identity is the node's identity without secrets, as delivered to the node.
type Identity struct {
	Hostname string `json:"hostname"`
	// Cluster and Role are the cluster and the role the node's image is built for; the installer
	// refuses an image of others.
	Cluster string `json:"cluster"`
	Role    string `json:"role"`
	// Platform is the platform the node's image is built for.
	Platform string         `json:"platform"`
	Network  map[string]any `json:"network"`
	// NetworkUnits are the systemd-networkd unit files rendered from Network, by file name.
	NetworkUnits map[string]string `json:"networkUnits"`
	Labels       map[string]string `json:"labels"`
	Taints       []Taint           `json:"taints"`
	// Storage is how the node partitions, encrypts and mounts its disks.
	Storage    storage.Section     `json:"storage"`
	Kubernetes *KubernetesIdentity `json:"kubernetes"`
	// Time is how the node keeps its clock.
	Time       Time                       `json:"time"`
	Extensions map[string]json.RawMessage `json:"extensions"`
}

// Time names the servers the node keeps its clock with.
type Time struct {
	Servers []TimeServer `json:"servers"`
}

// TimeServer is a time server; Port zero means NTP's.
type TimeServer struct {
	Host string `json:"host"`
	NTS  bool   `json:"nts"`
	Port int    `json:"port,omitempty"`
}

// UnmarshalJSON reads a time server, authenticated with NTS unless "nts" is false, as the Nix
// option defaults to: a server is never left unauthenticated because a field was left out.
func (s *TimeServer) UnmarshalJSON(data []byte) error {
	type plain TimeServer
	server := plain{NTS: true}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&server); err != nil {
		return err
	}
	*s = TimeServer(server)
	return nil
}

// timeHost is what a time server's host may be: a host name or an address.
var timeHost = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]*$`)

// Check checks that each server has a host name or address and a port, if any, that is one.
func (t Time) Check() error {
	for _, s := range t.Servers {
		if !timeHost.MatchString(s.Host) {
			return fmt.Errorf("time server %q is not a host name or address", s.Host)
		}
		if s.Port < 0 || s.Port > 65535 {
			return fmt.Errorf("time server %s: port %d is out of range", s.Host, s.Port)
		}
	}
	return nil
}

// ChronySources are the servers as chrony's sources file lists them.
func (t Time) ChronySources() (string, error) {
	if err := t.Check(); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, s := range t.Servers {
		b.WriteString("server " + s.Host + " iburst")
		if s.NTS {
			b.WriteString(" nts")
		}
		if s.Port != 0 {
			fmt.Fprintf(&b, " port %d", s.Port)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// KubernetesIdentity is what the node is in Kubernetes; nil on nodes of a role without
// Kubernetes.
type KubernetesIdentity struct {
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

// CheckNodeIPs checks that the fixed addresses are addresses.
func (k KubernetesIdentity) CheckNodeIPs() error {
	for _, ip := range k.NodeIPs {
		if _, err := netip.ParseAddr(ip); err != nil {
			return fmt.Errorf("nodeIPs: %q is not an address", ip)
		}
	}
	return nil
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
