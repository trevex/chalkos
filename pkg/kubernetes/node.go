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
	Taints    []identity.Taint
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
	if id.Kubernetes == nil || id.Kubernetes.NodeName == "" {
		return Node{}, errors.New("the identity names no Kubernetes node")
	}
	n := Node{Name: id.Kubernetes.NodeName, Hostname: id.Hostname, ValidSubnets: id.Kubernetes.ValidSubnets, Labels: id.Labels, Taints: id.Taints}
	for _, s := range id.Kubernetes.NodeIPs {
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
