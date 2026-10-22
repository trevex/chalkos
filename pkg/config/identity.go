package config

import (
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
)

// Target is the image a set is translated for.
type Target struct {
	// Platform is the image's, as its os-release names it.
	Platform string
	// Image is the image's Kubernetes facts, its image.json, which the cluster its documents give
	// is built with; nil on an image without Kubernetes.
	Image *kubernetes.Image
}

// Identity translates the set into the identity of the node called name for the image t describes,
// as the cluster definition rendered it, so every reader of the identity on the node keeps
// working. Extension kinds stay in the set. The name is the node's name in the inventory, which
// its node certificate carries; the hostname defaults to it without a Host that gives one, the
// Kubernetes node is called by it and the storage seeds derive from it, whatever the Node document
// is called.
func (s *Set) Identity(name string, t Target) (identity.Identity, error) {
	if name == "" {
		return identity.Identity{}, fmt.Errorf("the node has no name to translate its config for")
	}
	sec, err := s.storage(name)
	if err != nil {
		return identity.Identity{}, err
	}
	var networkDocs []Document
	for _, kind := range []string{"Link", "NetDev", "Network"} {
		networkDocs = append(networkDocs, s.Of(NetworkdAPI, kind)...)
	}
	id := identity.Identity{
		Hostname: name,
		Platform: t.Platform,
		Network:  NetworkMap(networkDocs),
		Labels:   map[string]string{},
		Taints:   []identity.Taint{},
		Storage:  sec,
	}
	if hostDoc, ok := s.One("Host"); ok {
		var host hostSpec
		if err := hostDoc.decodeSpec(&host); err != nil {
			return identity.Identity{}, err
		}
		if host.Hostname != nil {
			id.Hostname = *host.Hostname
		}
	}
	if t.Image != nil {
		// The node's addresses follow the families and nodeIP.validSubnets of the cluster the
		// documents give, which needs a Node and a Cluster document.
		c, err := s.Cluster(*t.Image)
		if err != nil {
			return identity.Identity{}, fmt.Errorf("the Kubernetes cluster the documents give: %w", err)
		}
		if err := s.kubelet(&id, name, c); err != nil {
			return identity.Identity{}, err
		}
	}
	return id, nil
}

// The labels a kubelet may set on its own Node (k8s.io/kubelet's IsKubeletLabel in Kubernetes
// 1.37): it refuses other labels in the kubernetes.io and k8s.io namespaces and does not start.
var kubeletLabels = []string{
	"kubernetes.io/hostname", "topology.kubernetes.io/zone", "topology.kubernetes.io/region",
	"failure-domain.beta.kubernetes.io/zone", "failure-domain.beta.kubernetes.io/region",
	"beta.kubernetes.io/instance-type", "node.kubernetes.io/instance-type", "kubernetes.io/os",
	"kubernetes.io/arch", "beta.kubernetes.io/os", "beta.kubernetes.io/arch",
}

var kubeletLabelNamespaces = []string{"kubelet.kubernetes.io", "node.kubernetes.io"}

func inNamespace(namespace, ns string) bool {
	return namespace == ns || strings.HasSuffix(namespace, "."+ns)
}

func refusedLabel(key string) bool {
	namespace, _, found := strings.Cut(key, "/")
	if !found {
		namespace = ""
	}
	in := func(namespaces ...string) bool {
		return slices.ContainsFunc(namespaces, func(ns string) bool { return inNamespace(namespace, ns) })
	}
	return in("kubernetes.io", "k8s.io") && !slices.Contains(kubeletLabels, key) && !in(kubeletLabelNamespaces...)
}

// kubelet fills in the node's Kubernetes settings from its Node document, which the cluster c
// was built with, and refuses settings the kubelet would not start with. The errors come in this
// order: refused labels and swap volumes by name, then the node's addresses.
func (s *Set) kubelet(id *identity.Identity, name string, c kubernetes.Cluster) error {
	var spec nodeSpec
	if d, ok := s.One("Node"); ok {
		if err := d.decodeSpec(&spec); err != nil {
			return err
		}
	}
	families, err := c.Families()
	if err != nil {
		return err
	}
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

	labels := make([]string, 0, len(spec.Labels))
	for key, value := range spec.Labels {
		labels = append(labels, key)
		id.Labels[key] = value
	}
	sort.Strings(labels)
	for _, key := range labels {
		if refusedLabel(key) {
			add("Node: label %q: the kubelet sets labels in the kubernetes.io and k8s.io namespaces on its own node only under kubelet.kubernetes.io/ and node.kubernetes.io/ or from the list %s", key, strings.Join(kubeletLabels, ", "))
		}
	}
	for _, t := range spec.Taints {
		id.Taints = append(id.Taints, identity.Taint{Key: t.Key, Value: deref(t.Value), Effect: t.Effect})
	}
	volumes := make([]string, 0, len(id.Storage.Volumes))
	for v := range id.Storage.Volumes {
		volumes = append(volumes, v)
	}
	sort.Strings(volumes)
	for _, v := range volumes {
		if id.Storage.Volumes[v].Format == "swap" {
			add("Volume %s: a swap volume on a Kubernetes node; the kubelet refuses to run with swap (failSwapOn)", v)
		}
	}

	subnets := c.NodeIP.ValidSubnets
	// The node's own subnets replace the cluster's as a whole.
	if spec.NodeIP.ValidSubnets != nil {
		subnets = spec.NodeIP.ValidSubnets
	}
	_, subnetErr := nodeip.ParseFilter(spec.NodeIP.ValidSubnets)
	if subnetErr != nil {
		add("Node: nodeIP: %v", subnetErr)
	}
	nodeIPs := spec.NodeIP.Addresses
	if nodeIPs == nil {
		nodeIPs = defaultNodeIPs(id.StaticAddresses(), families, subnets)
	}
	var seen []nodeip.Family
	var addrs []netip.Addr
	for _, a := range nodeIPs {
		if ip, err := netip.ParseAddr(a); err == nil {
			seen = append(seen, nodeip.FamilyOf(ip))
			addrs = append(addrs, ip)
		} else {
			add("Node: nodeIP.addresses: %q is not an address", a)
		}
	}
	var unique []nodeip.Family
	for _, f := range seen {
		if !slices.Contains(unique, f) {
			unique = append(unique, f)
		}
	}
	for _, f := range unique {
		if count(seen, f) > 1 {
			add("Node: nodeIP.addresses: more than one %s address; give at most one per family", f)
		}
	}
	for _, f := range unique {
		if !slices.Contains(families, f) {
			add("Node: nodeIP.addresses: an %s address, but the cluster's ipFamilies are [ %s ]", f, joinFamilies(families))
		}
	}
	// The other nodes take VXLAN only from vxlanSourceSubnets, so every address the node has or
	// may pick must lie in them; the node checks the addresses it picks at boot again. Invalid
	// sources are the Flannel document's error, reported with the cluster.
	if sources, err := c.VXLANSources(); err == nil {
		if err := kubernetes.CheckVXLANSources(sources, addrs); err != nil {
			add("Node: nodeIP.addresses: %v", err)
		}
		for _, s := range subnets {
			p, err := netip.ParsePrefix(s)
			if err == nil && !kubernetes.InVXLANSources(sources, p) {
				add("Node: the node picks its address from %s, which is not inside vxlanSourceSubnets, from which alone the other nodes take VXLAN", s)
			}
		}
	}
	// Invalid subnets are reported above; which families they would pick from is moot.
	if subnetErr == nil {
		for _, f := range families {
			if !slices.Contains(seen, f) && !picks(subnets, f) {
				add("Node: no %s address in nodeIP.addresses, and no %s subnet in the nodeIP.validSubnets that apply, so the node cannot pick its %s address", f, f, f)
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid Kubernetes settings:\n- %s", strings.Join(errs, "\n- "))
	}
	id.Kubernetes = &identity.Kubernetes{NodeName: name, NodeIPs: nodeIPs, ValidSubnets: spec.NodeIP.ValidSubnets}
	return nil
}

// defaultNodeIPs are the first static address of each of the cluster's families, or none when
// subnets apply to the node, which then picks every address at boot.
func defaultNodeIPs(static []string, families []nodeip.Family, subnets []string) []string {
	nodeIPs := []string{}
	if len(subnets) > 0 {
		return nodeIPs
	}
	for _, f := range families {
		for _, a := range static {
			if ip, err := netip.ParseAddr(a); err == nil && nodeip.FamilyOf(ip) == f {
				nodeIPs = append(nodeIPs, a)
				break
			}
		}
	}
	return nodeIPs
}

// picks tells whether a node picks an address of family f from subnets: when they include one of
// f or include none at all.
func picks(subnets []string, f nodeip.Family) bool {
	anyIncluded := false
	for _, s := range subnets {
		if strings.HasPrefix(s, "!") {
			continue
		}
		anyIncluded = true
		if p, err := netip.ParsePrefix(s); err == nil && nodeip.FamilyOf(p.Addr()) == f {
			return true
		}
	}
	return !anyIncluded
}

func count(families []nodeip.Family, f nodeip.Family) int {
	n := 0
	for _, g := range families {
		if g == f {
			n++
		}
	}
	return n
}

func joinFamilies(families []nodeip.Family) string {
	out := make([]string, len(families))
	for i, f := range families {
		out[i] = string(f)
	}
	return strings.Join(out, " ")
}
