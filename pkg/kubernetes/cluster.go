// Package kubernetes describes a chalkos node's Kubernetes cluster as its image records it in
// /etc/chalkos/kubernetes/cluster.json, and the node itself as its identity names it.
package kubernetes

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"net/url"
	"os"
	"slices"

	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	"github.com/trevex/chalkos/pkg/manifest"
)

// Kinds of Kubernetes nodes.
const (
	KindControlPlane = manifest.KindControlPlane
	KindWorker       = manifest.KindWorker
)

// Cluster is what the node's image knows about its cluster.
type Cluster struct {
	// Kind is what the image's nodes are: KindControlPlane or KindWorker.
	Kind string `json:"kind"`
	// Endpoint is the API server's URL, such as https://10.0.0.10:6443.
	Endpoint string `json:"endpoint"`
	// Version is the Kubernetes release, such as 1.37.1.
	Version                        string `json:"version"`
	PodCIDR                        string `json:"podCIDR"`
	ServiceCIDR                    string `json:"serviceCIDR"`
	DNSIP                          string `json:"dnsIP"`
	Domain                         string `json:"domain"`
	AllowSchedulingOnControlPlanes bool   `json:"allowSchedulingOnControlPlanes"`
	// IPFamilies are the address families nodes have an address of, ipv4 or ipv6, the primary
	// one first. Empty means ipv4.
	IPFamilies []string `json:"ipFamilies"`
	// NodeIP is how nodes without a fixed address pick theirs.
	NodeIP NodeIP `json:"nodeIP"`
	// VIP is the virtual IP one healthy control-plane node holds at a time.
	VIP VIP `json:"vip"`
	// ExtraArgs are flags of the control-plane components by component name, such as
	// kube-apiserver, without leading dashes.
	ExtraArgs map[string]map[string]string `json:"extraArgs"`
	Images    Images                       `json:"images"`
}

// NodeIP is how a node picks its address.
type NodeIP struct {
	// ValidSubnets are subnets in CIDR notation to pick the address from; a leading "!" excludes
	// one. Empty picks any address.
	ValidSubnets []string `json:"validSubnets"`
	// Timeout is how many seconds a node waits for its address.
	Timeout int `json:"timeout"`
}

// VIP is the cluster's virtual IP.
type VIP struct {
	// Addresses are the virtual addresses, at most one per family; none means no virtual IP.
	Addresses []string `json:"addresses"`
	// Mode is how the holder announces them: l2 adds them to an interface and announces them
	// with gratuitous ARP and unsolicited neighbour advertisements.
	Mode string `json:"mode"`
	// Interface is the interface that carries them; empty means the one with the node's address
	// of their family.
	Interface string `json:"interface"`
}

// Images are the control plane's images.
type Images struct {
	Etcd                  string `json:"etcd"`
	KubeAPIServer         string `json:"kubeAPIServer"`
	KubeControllerManager string `json:"kubeControllerManager"`
	KubeScheduler         string `json:"kubeScheduler"`
}

// ReadCluster reads and checks the image's cluster file.
func ReadCluster(path string) (Cluster, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Cluster{}, err
	}
	var c Cluster
	if err := json.Unmarshal(data, &c); err != nil {
		return Cluster{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Cluster{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// ProtectedAPIServerFlags decide who may do what on the API server; chalkos sets them and
// extra flags must not override them.
var ProtectedAPIServerFlags = []string{"anonymous-auth", "authentication-config", "authorization-mode", "enable-bootstrap-token-auth"}

// Validate checks the kind, the endpoint, the address ranges, how nodes pick their address and
// the extra flags.
func (c Cluster) Validate() error {
	for _, flag := range ProtectedAPIServerFlags {
		if _, ok := c.ExtraArgs["kube-apiserver"][flag]; ok {
			return fmt.Errorf("extraArgs: kube-apiserver --%s is set by chalkos and cannot be overridden", flag)
		}
	}
	if c.Kind != KindControlPlane && c.Kind != KindWorker {
		return fmt.Errorf("unknown kind %q", c.Kind)
	}
	if _, err := c.EndpointHost(); err != nil {
		return err
	}
	if _, err := c.APIServerServiceIP(); err != nil {
		return err
	}
	if _, _, err := net.ParseCIDR(c.PodCIDR); err != nil {
		return fmt.Errorf("podCIDR: %w", err)
	}
	if net.ParseIP(c.DNSIP) == nil {
		return fmt.Errorf("dnsIP %q is not an address", c.DNSIP)
	}
	if c.Domain == "" {
		return errors.New("no cluster domain")
	}
	if _, err := nodeip.ParseFilter(c.NodeIP.ValidSubnets); err != nil {
		return fmt.Errorf("nodeIP: %w", err)
	}
	if c.NodeIP.Timeout < 0 {
		return fmt.Errorf("nodeIP: the timeout %d is negative", c.NodeIP.Timeout)
	}
	families, err := c.Families()
	if err != nil {
		return err
	}
	vips, err := c.VIPAddresses()
	if err != nil {
		return err
	}
	if len(vips) == 0 {
		return nil
	}
	if c.VIP.Mode != "l2" {
		return fmt.Errorf("vip: unknown mode %q", c.VIP.Mode)
	}
	if err := onePerFamily("vip", vips, families); err != nil {
		return err
	}
	host, _ := c.EndpointHost()
	if ip, err := netip.ParseAddr(host); err == nil && !slices.Contains(vips, ip.Unmap()) {
		return fmt.Errorf("vip: the endpoint %s is none of the virtual addresses", c.Endpoint)
	}
	return nil
}

// Families returns the address families of the cluster's nodes, the primary one first.
func (c Cluster) Families() ([]nodeip.Family, error) {
	if len(c.IPFamilies) == 0 {
		return []nodeip.Family{nodeip.IPv4}, nil
	}
	var families []nodeip.Family
	for _, s := range c.IPFamilies {
		f, err := nodeip.ParseFamily(s)
		if err != nil {
			return nil, fmt.Errorf("ipFamilies: %w", err)
		}
		if slices.Contains(families, f) {
			return nil, fmt.Errorf("ipFamilies: %s is listed twice", f)
		}
		families = append(families, f)
	}
	return families, nil
}

// VIPAddresses returns the virtual addresses.
func (c Cluster) VIPAddresses() ([]netip.Addr, error) {
	var vips []netip.Addr
	for _, s := range c.VIP.Addresses {
		ip, err := netip.ParseAddr(s)
		if err != nil || ip.Zone() != "" {
			return nil, fmt.Errorf("vip: %q is not an address", s)
		}
		vips = append(vips, ip.Unmap())
	}
	return vips, nil
}

// onePerFamily checks that the addresses are of the families, at most one of each.
func onePerFamily(what string, ips []netip.Addr, families []nodeip.Family) error {
	seen := map[nodeip.Family]netip.Addr{}
	for _, ip := range ips {
		f := nodeip.FamilyOf(ip)
		if !slices.Contains(families, f) {
			return fmt.Errorf("%s: %s is an %s address, but the cluster's families are %v", what, ip, f, families)
		}
		if other, ok := seen[f]; ok {
			return fmt.Errorf("%s: %s and %s are both %s addresses; give at most one of each family", what, other, ip, f)
		}
		seen[f] = ip
	}
	return nil
}

// EndpointHost returns the host of the endpoint, a name or an address.
func (c Cluster) EndpointHost() (string, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", fmt.Errorf("endpoint %q is not an https URL", c.Endpoint)
	}
	return u.Hostname(), nil
}

// APIServerServiceIP returns the first address of the service range, which the kubernetes
// service gets.
func (c Cluster) APIServerServiceIP() (net.IP, error) {
	_, cidr, err := net.ParseCIDR(c.ServiceCIDR)
	if err != nil {
		return nil, fmt.Errorf("serviceCIDR: %w", err)
	}
	ip := cidr.IP
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	n := new(big.Int).Add(new(big.Int).SetBytes(ip), big.NewInt(1))
	out := make(net.IP, len(ip))
	n.FillBytes(out)
	return out, nil
}

// Node is this node as its identity describes it.
type Node struct {
	// Name is the node's name in the cluster and in Kubernetes.
	Name string
	// Hostname is the node's host name, which may differ from its name.
	Hostname string
	// FixedIPs are the node's fixed addresses, at most one per family; the node picks the
	// addresses of the other families at boot.
	FixedIPs []net.IP
	// IPs are the addresses the node picked at boot, one per family, the primary family's first;
	// empty in a node read from its identity.
	IPs []net.IP
	// ValidSubnets are the subnets the node picks its address from instead of its cluster's;
	// nil uses the cluster's.
	ValidSubnets []string
	// Addresses are the node's static addresses.
	Addresses []net.IP
	Labels    map[string]string
	Taints    []manifest.Taint
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

// ParseNode reads the node from its identity.
func ParseNode(identity []byte) (Node, error) {
	var id manifest.Identity
	if err := json.Unmarshal(identity, &id); err != nil {
		return Node{}, fmt.Errorf("parse the identity: %w", err)
	}
	if id.Kubernetes == nil || id.Kubernetes.NodeName == "" {
		return Node{}, errors.New("the identity names no Kubernetes node")
	}
	n := Node{Name: id.Kubernetes.NodeName, Hostname: id.Hostname, ValidSubnets: id.Kubernetes.ValidSubnets, Labels: id.Labels, Taints: id.Taints}
	for _, s := range id.Kubernetes.NodeIPs {
		ip := net.ParseIP(s)
		if ip == nil {
			return Node{}, fmt.Errorf("nodeIPs: %q is not an address", s)
		}
		n.FixedIPs = append(n.FixedIPs, ip)
	}
	if _, err := nodeip.ParseFilter(n.ValidSubnets); err != nil {
		return Node{}, err
	}
	for _, a := range id.StaticAddresses() {
		if ip := net.ParseIP(a); ip != nil {
			n.Addresses = append(n.Addresses, ip)
		}
	}
	return n, nil
}

// CertificateIPs returns the addresses the node picked and its static addresses, each once.
func (n Node) CertificateIPs() []net.IP {
	var ips []net.IP
	for _, ip := range append(slices.Clone(n.IPs), n.Addresses...) {
		seen := false
		for _, have := range ips {
			seen = seen || have.Equal(ip)
		}
		if !seen {
			ips = append(ips, ip)
		}
	}
	return ips
}

// NodeIPSelector is how node n picks its addresses, one of each of the cluster's families: its
// fixed address of the family, or else the first address in the subnets of its identity or,
// without those, of its cluster. Addresses in the pod and service ranges are never picked, and
// the endpoint's and the virtual addresses only when no other matches.
func (c Cluster) NodeIPSelector(n Node) (nodeip.Selector, error) {
	families, err := c.Families()
	if err != nil {
		return nodeip.Selector{}, err
	}
	subnets := c.NodeIP.ValidSubnets
	if n.ValidSubnets != nil {
		subnets = n.ValidSubnets
	}
	filter, err := nodeip.ParseFilter(subnets)
	if err != nil {
		return nodeip.Selector{}, err
	}
	s := nodeip.Selector{Families: families, Filter: filter}
	for _, ip := range n.FixedIPs {
		fixed, ok := netip.AddrFromSlice(ip)
		if !ok {
			return nodeip.Selector{}, fmt.Errorf("nodeIP %v is not an address", ip)
		}
		s.Fixed = append(s.Fixed, fixed.Unmap())
	}
	if err := onePerFamily("nodeIPs", s.Fixed, families); err != nil {
		return nodeip.Selector{}, err
	}
	for _, cidr := range []string{c.PodCIDR, c.ServiceCIDR} {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			return nodeip.Selector{}, fmt.Errorf("%q is not an address range: %w", cidr, err)
		}
		s.Reserved = append(s.Reserved, prefix.Masked())
	}
	host, err := c.EndpointHost()
	if err != nil {
		return nodeip.Selector{}, err
	}
	// A host name is not resolved: its address may change, and the node may not reach DNS yet.
	if ip, err := netip.ParseAddr(host); err == nil {
		s.Last = append(s.Last, ip.Unmap().WithZone(""))
	}
	vips, err := c.VIPAddresses()
	if err != nil {
		return nodeip.Selector{}, err
	}
	s.Last = append(s.Last, vips...)
	return s, nil
}
