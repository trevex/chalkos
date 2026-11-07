package kubernetes

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"

	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	"github.com/trevex/chalkos/pkg/layers"
)

// Node is this node as its identity describes it.
type Node struct {
	// Name is the node's name in the cluster and in Kubernetes.
	Name string
	// Hostname is the node's host name, which may differ from its name.
	Hostname string
	// FixedIPs are the node's fixed addresses, at most one per family; the node picks the
	// addresses of the other families at boot.
	FixedIPs []netip.Addr
	// IPs are the addresses the node picked at boot, one per family, the primary family's first;
	// empty in a node read from its identity.
	IPs []netip.Addr
	// ValidSubnets are the subnets the node picks its address from instead of its cluster's;
	// nil uses the cluster's.
	ValidSubnets []string
	// Addresses are the node's static addresses.
	Addresses []netip.Addr
	Labels    map[string]string
	Taints    []Taint
}

// NodeIdentity is what the node is in Kubernetes: the Kubernetes layer's part of its identity.
type NodeIdentity struct {
	// NodeName is the node's name in the cluster, which its Node object and kubelet
	// certificates carry.
	NodeName string `json:"nodeName"`
	// NodeIPs are the node's fixed addresses, at most one per address family; the node picks
	// the addresses of the other families at boot.
	NodeIPs []string `json:"nodeIPs"`
	// ValidSubnets are the subnets the node picks its addresses from instead of its cluster's;
	// nil uses the cluster's.
	ValidSubnets []string          `json:"validSubnets"`
	Labels       map[string]string `json:"labels"`
	Taints       []Taint           `json:"taints"`
}

// Taint is a Kubernetes taint applied to the node.
type Taint struct {
	Key string `json:"key"`
	// Value is empty for a taint without a value.
	Value  string `json:"value"`
	Effect string `json:"effect"`
}

// SetNodeIdentity stores n as the Kubernetes layer's part of the identity id.
func SetNodeIdentity(id *identity.Identity, n NodeIdentity) error {
	return id.SetLayer(layers.Kubernetes, n)
}

// NodeIdentityOf reads the Kubernetes layer's part of the identity id and tells whether it has
// one, which an identity on an image without Kubernetes does not.
func NodeIdentityOf(id identity.Identity) (NodeIdentity, bool, error) {
	var n NodeIdentity
	ok, err := id.Layer(layers.Kubernetes, &n)
	return n, ok, err
}

// ReadNode reads the node from its identity without secrets (node.json).
func ReadNode(path string) (Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Node{}, err
	}
	n, err := ParseNode(data)
	if err != nil {
		return Node{}, fmt.Errorf("%s: %w", path, err)
	}
	return n, nil
}

// ParseNode reads the node from its identity. IPv4-mapped addresses are stored in their IPv4
// form.
func ParseNode(data []byte) (Node, error) {
	var id identity.Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return Node{}, fmt.Errorf("parse the identity: %w", err)
	}
	k, ok, err := NodeIdentityOf(id)
	if err != nil {
		return Node{}, err
	}
	if !ok || k.NodeName == "" {
		return Node{}, errors.New("the identity names no Kubernetes node")
	}
	n := Node{Name: k.NodeName, Hostname: id.Hostname, ValidSubnets: k.ValidSubnets, Labels: k.Labels, Taints: k.Taints}
	for _, s := range k.NodeIPs {
		ip, err := netip.ParseAddr(s)
		if err != nil || ip.Zone() != "" {
			return Node{}, fmt.Errorf("nodeIPs: %q is not an address", s)
		}
		n.FixedIPs = append(n.FixedIPs, ip.Unmap())
	}
	if _, err := nodeip.ParseFilter(n.ValidSubnets); err != nil {
		return Node{}, err
	}
	for _, a := range id.StaticAddresses() {
		if ip, err := netip.ParseAddr(a); err == nil && ip.Zone() == "" {
			n.Addresses = append(n.Addresses, ip.Unmap())
		}
	}
	return n, nil
}

// CertificateIPs returns the addresses the node picked and its static addresses, each once.
func (n Node) CertificateIPs() []netip.Addr {
	var ips []netip.Addr
	for _, ip := range append(slices.Clone(n.IPs), n.Addresses...) {
		if !slices.Contains(ips, ip) {
			ips = append(ips, ip)
		}
	}
	return ips
}

// NodeIPSelector is how node n picks its addresses, one of each of the cluster's families: its
// fixed address of the family, or else the first address in the subnets of its identity or,
// without those, of its cluster. Addresses in the pod and service ranges of every family are
// never picked, and the endpoint's and the virtual addresses only when no other matches.
func (c Cluster) NodeIPSelector(n Node) (nodeip.Selector, error) {
	families := c.Families()
	subnets := c.NodeIP.ValidSubnets
	if n.ValidSubnets != nil {
		subnets = n.ValidSubnets
	}
	filter, err := nodeip.ParseFilter(subnets)
	if err != nil {
		return nodeip.Selector{}, err
	}
	// flannel uses the interface of the primary family's address for every family.
	s := nodeip.Selector{Families: families, Filter: filter, SameInterface: c.Flannel != nil && len(families) > 1}
	s.Fixed = slices.Clone(n.FixedIPs)
	if err := onePerFamily("nodeIPs", s.Fixed, families); err != nil {
		return nodeip.Selector{}, err
	}
	s.Reserved = append(c.PodCIDRList(), c.ServiceCIDRList()...)
	// A host name is not resolved: its address may change, and the node may not reach DNS yet.
	if ip, err := netip.ParseAddr(c.EndpointHost()); err == nil {
		s.Last = append(s.Last, ip.Unmap().WithZone(""))
	}
	s.Last = append(s.Last, c.VIPAddresses()...)
	return s, nil
}
