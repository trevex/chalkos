// Package kubernetes describes a chalkos node's Kubernetes cluster, as chalkd builds it from the
// node's config and its image's facts, and the node itself as its identity names it.
package kubernetes

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"slices"

	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
)

// Kinds of Kubernetes nodes.
const (
	KindControlPlane = "controlplane"
	KindWorker       = "worker"
)

// Cluster is the node's Kubernetes cluster. chalkd builds it from the node's config and the
// image's Kubernetes facts (image.json) and writes it to ClusterFile.
type Cluster struct {
	// Kind is the node's kind: KindControlPlane or KindWorker.
	Kind string `json:"kind"`
	// Endpoint is the API server's URL, such as https://10.0.0.10:6443.
	Endpoint string `json:"endpoint"`
	// Version is the Kubernetes release, such as 1.37.1.
	Version                        string `json:"version"`
	Domain                         string `json:"domain"`
	AllowSchedulingOnControlPlanes bool   `json:"allowSchedulingOnControlPlanes"`
	// IPFamilies are the address families nodes have an address of, the primary one first.
	// Empty means ipv4.
	IPFamilies []nodeip.Family `json:"ipFamilies"`
	// PodCIDRs and ServiceCIDRs are the pod and service ranges of each family, DNSIPs the cluster
	// DNS's service address and NodeCIDRMaskSizes the prefix length of each node's part of the
	// pod range. Only the entries of IPFamilies count.
	PodCIDRs          ByFamily[netip.Prefix] `json:"podCIDRs"`
	ServiceCIDRs      ByFamily[netip.Prefix] `json:"serviceCIDRs"`
	DNSIPs            ByFamily[netip.Addr]   `json:"dnsIPs"`
	NodeCIDRMaskSizes ByFamily[int]          `json:"nodeCIDRMaskSizes"`
	// NodeIP is how nodes without a fixed address pick theirs.
	NodeIP NodeIP `json:"nodeIP"`
	// Flannel is flannel's configuration when it is the pod network; nil otherwise.
	Flannel *Flannel `json:"flannel"`
	// VIP is the virtual IP one healthy control-plane node holds at a time.
	VIP VIP `json:"vip"`
	// ExtraArgs are flags of the components by component name, such as kube-apiserver or
	// kubelet: the kubelet's from the node's Node document, the others' from the Cluster document.
	// Their names are letters, digits, _, . and -, starting with a letter or digit, without the
	// leading dashes; the protected flags are refused.
	ExtraArgs map[string]map[string]string `json:"extraArgs"`
	Images    Images                       `json:"images"`
}

// ByFamily holds a value of each address family.
type ByFamily[T any] struct {
	IPv4 T `json:"ipv4"`
	IPv6 T `json:"ipv6"`
}

// Of returns the value of family f.
func (b ByFamily[T]) Of(f nodeip.Family) T {
	if f == nodeip.IPv6 {
		return b.IPv6
	}
	return b.IPv4
}

// NodeIP is how a node picks its address.
type NodeIP struct {
	// ValidSubnets are subnets in CIDR notation to pick the address from; a leading "!" excludes
	// one. Empty picks any address.
	ValidSubnets []string `json:"validSubnets"`
	// Timeout is how many seconds a node waits for its address.
	Timeout int `json:"timeout"`
}

// Flannel is how flannel connects the nodes' pods.
type Flannel struct {
	// MTU is the MTU flannel's VXLAN assumes for the network between the nodes; its VXLAN
	// devices and the pods get 50 less. Zero takes the MTU of the interface holding the node's
	// address.
	MTU int `json:"mtu"`
	// VXLANSourceSubnets are the ranges flannel's VXLAN must come from, at least one of each family
	// and none overlapping the pod or service ranges; empty takes it from any source.
	VXLANSourceSubnets []netip.Prefix `json:"vxlanSourceSubnets"`
}

// VIP is the cluster's virtual IP.
type VIP struct {
	// Addresses are the virtual addresses, at most one per family; none means no virtual IP.
	Addresses []netip.Addr `json:"addresses"`
	// Mode is how the holder announces them: l2 adds them to an interface and announces them
	// with gratuitous ARP and unsolicited neighbour advertisements.
	Mode string `json:"mode"`
	// Interface is the interface that carries them on this node, from its Node document; empty
	// means the one with the node's address of their family.
	Interface string `json:"interface"`
}

// Images are the images the cluster's components run from: the control plane's static pods and
// the add-ons chalkd applies. Flannel is empty without flannel.
type Images struct {
	Etcd                  string `json:"etcd"`
	KubeAPIServer         string `json:"kubeAPIServer"`
	KubeControllerManager string `json:"kubeControllerManager"`
	KubeScheduler         string `json:"kubeScheduler"`
	KubeProxy             string `json:"kubeProxy"`
	CoreDNS               string `json:"coredns"`
	Flannel               string `json:"flannel"`
}

// ReadCluster reads and checks a cluster file.
func ReadCluster(path string) (Cluster, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Cluster{}, err
	}
	c, err := parseCluster(data)
	if err != nil {
		return Cluster{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// parseCluster parses and checks a cluster file's contents.
func parseCluster(data []byte) (Cluster, error) {
	var c Cluster
	if err := json.Unmarshal(data, &c); err != nil {
		return Cluster{}, fmt.Errorf("parse: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Cluster{}, err
	}
	return c, nil
}

// Validate checks the endpoint, the extra flags, the kind, how nodes pick their address, the
// address families and their ranges, flannel's settings and the VIPs.
func (c Cluster) Validate() error {
	if _, err := parseEndpoint(c.Endpoint); err != nil {
		return err
	}
	if err := c.validateExtraArgs(); err != nil {
		return err
	}
	if c.Kind != KindControlPlane && c.Kind != KindWorker {
		return fmt.Errorf("unknown kind %q", c.Kind)
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
	if err := c.validateFamilies(); err != nil {
		return err
	}
	families := c.Families()
	if err := c.validateFlannel(families); err != nil {
		return err
	}
	for _, f := range families {
		if err := c.validateRanges(f); err != nil {
			return err
		}
	}
	if err := c.validateVXLANSources(families); err != nil {
		return err
	}
	return c.validateVIP(families)
}

// validateFamilies refuses unknown families and families listed twice.
func (c Cluster) validateFamilies() error {
	for i, f := range c.IPFamilies {
		if _, err := nodeip.ParseFamily(string(f)); err != nil {
			return fmt.Errorf("ipFamilies: %w", err)
		}
		if slices.Contains(c.IPFamilies[:i], f) {
			return fmt.Errorf("ipFamilies: %s is listed twice", f)
		}
	}
	return nil
}

// validateFlannel checks flannel's MTU: pods get 50 less, which must leave them their family's
// minimum MTU, IPv6's 1280 when IPv6 is in use and IPv4's 576 otherwise.
func (c Cluster) validateFlannel(families []nodeip.Family) error {
	if c.Flannel == nil || c.Flannel.MTU == 0 {
		return nil
	}
	name, minimum := "IPv4", 576
	if slices.Contains(families, nodeip.IPv6) {
		name, minimum = "IPv6", 1280
	}
	if c.Flannel.MTU < minimum+50 {
		return fmt.Errorf("flannel: the MTU %d is below the minimum of %d: pods get 50 less, and %s needs an MTU of at least %d", c.Flannel.MTU, minimum+50, name, minimum)
	}
	return nil
}

// validateVIP checks the virtual addresses: at most one of each of the cluster's families, a
// known mode, and the endpoint's address among them when the endpoint names one.
func (c Cluster) validateVIP(families []nodeip.Family) error {
	for _, ip := range c.VIP.Addresses {
		if !ip.IsValid() || ip.Zone() != "" {
			return fmt.Errorf("vip: %q is not an address", ip)
		}
	}
	vips := c.VIPAddresses()
	if len(vips) == 0 {
		return nil
	}
	if c.VIP.Mode != "l2" {
		return fmt.Errorf("vip: unknown mode %q", c.VIP.Mode)
	}
	if err := onePerFamily("vip", vips, families); err != nil {
		return err
	}
	if ip, err := netip.ParseAddr(c.EndpointHost()); err == nil && !slices.Contains(vips, ip.Unmap()) {
		return fmt.Errorf("vip: the endpoint %s is none of the virtual addresses", c.Endpoint)
	}
	return nil
}

// Families returns the address families of the cluster's nodes, the primary one first.
func (c Cluster) Families() []nodeip.Family {
	if len(c.IPFamilies) == 0 {
		return []nodeip.Family{nodeip.IPv4}
	}
	return slices.Clone(c.IPFamilies)
}

// Primary returns the primary address family, the first of IPFamilies. Every value that exists
// for one family only, such as the kubernetes service's address, follows it.
func (c Cluster) Primary() nodeip.Family {
	return c.Families()[0]
}

// Loopback returns the primary family's loopback address. The control plane's own connections on
// a node use it:
//   - etcd's local listeners and the API server's connection to etcd;
//   - the connections of the controller-manager, the scheduler and chalkd to the API server.
func (c Cluster) Loopback() netip.Addr {
	if c.Primary() == nodeip.IPv6 {
		return netip.IPv6Loopback()
	}
	return netip.AddrFrom4([4]byte{127, 0, 0, 1})
}

// LocalAPIServer is where components on a control-plane node reach their own API server.
func (c Cluster) LocalAPIServer() string {
	return httpsURL(c.Loopback(), APIServerPort)
}

// LocalEtcd is where the API server and chalkd on a control-plane node reach its etcd member.
func (c Cluster) LocalEtcd() string {
	return EtcdClientURL(c.Loopback())
}

// VIPAddresses returns the virtual addresses, IPv4-mapped ones in their IPv4 form.
func (c Cluster) VIPAddresses() []netip.Addr {
	var vips []netip.Addr
	for _, ip := range c.VIP.Addresses {
		vips = append(vips, ip.Unmap())
	}
	return vips
}

// parseEndpoint reads an endpoint, an https URL with a host.
func parseEndpoint(endpoint string) (*url.URL, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, fmt.Errorf("endpoint %q is not an https URL", endpoint)
	}
	return u, nil
}

// EndpointHost returns the host of the endpoint, a name or an address. It is empty for an
// endpoint Validate refuses.
func (c Cluster) EndpointHost() string {
	u, err := parseEndpoint(c.Endpoint)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// APIServerServiceIP returns the first address of the primary family's service range, which
// the kubernetes service gets: the API server creates it in that family alone.
func (c Cluster) APIServerServiceIP() netip.Addr {
	return c.ServiceCIDRs.Of(c.Primary()).Masked().Addr().Next()
}
