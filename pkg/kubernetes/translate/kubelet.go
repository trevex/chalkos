package translate

import (
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/internal/strs"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	"github.com/trevex/chalkos/pkg/storage"
)

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
func kubelet(s *config.Set, id *identity.Identity, name string, c kubernetes.Cluster) error {
	var spec nodeSpec
	if d, ok := s.One("Node"); ok {
		if err := d.DecodeSpec(&spec); err != nil {
			return err
		}
	}
	maps.Copy(id.Labels, spec.Labels)
	for _, t := range spec.Taints {
		id.Taints = append(id.Taints, identity.Taint{Key: t.Key, Value: deref(t.Value), Effect: t.Effect})
	}
	ips, ipErrs := nodeIPs(spec, c, id.StaticAddresses())
	errs := slices.Concat(checkLabels(spec.Labels), checkSwap(id.Storage.Volumes), ipErrs)
	if len(errs) > 0 {
		return fmt.Errorf("invalid Kubernetes settings:\n- %s", strings.Join(errs, "\n- "))
	}
	id.Kubernetes = &identity.Kubernetes{NodeName: name, NodeIPs: ips, ValidSubnets: spec.NodeIP.ValidSubnets}
	return nil
}

// checkLabels refuses the labels a kubelet may not set on its own Node, by key.
func checkLabels(labels map[string]string) []string {
	var errs []string
	for _, key := range slices.Sorted(maps.Keys(labels)) {
		if refusedLabel(key) {
			errs = append(errs, fmt.Sprintf("Node: label %q: the kubelet sets labels in the kubernetes.io and k8s.io namespaces on its own node only under kubelet.kubernetes.io/ and node.kubernetes.io/ or from the list %s", key, strings.Join(kubeletLabels, ", ")))
		}
	}
	return errs
}

// checkSwap refuses swap volumes, by name, which the kubelet does not run with.
func checkSwap(volumes map[string]storage.Volume) []string {
	var errs []string
	for _, v := range slices.Sorted(maps.Keys(volumes)) {
		if volumes[v].Format == "swap" {
			errs = append(errs, fmt.Sprintf("Volume %s: a swap volume on a Kubernetes node; the kubelet refuses to run with swap (failSwapOn)", v))
		}
	}
	return errs
}

// nodeIPs are the addresses the node of spec registers with in the cluster c: those its Node
// document gives, or else defaultNodeIPs of static. It also returns why they, or the subnets the
// node picks its addresses from, do not fit the cluster.
func nodeIPs(spec nodeSpec, c kubernetes.Cluster, static []string) ([]string, []string) {
	families := c.Families()
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

	subnets := c.NodeIP.ValidSubnets
	// The node's own subnets replace the cluster's as a whole.
	if spec.NodeIP.ValidSubnets != nil {
		subnets = spec.NodeIP.ValidSubnets
	}
	_, subnetErr := nodeip.ParseFilter(spec.NodeIP.ValidSubnets)
	if subnetErr != nil {
		add("Node: nodeIP: %v", subnetErr)
	}
	ips := spec.NodeIP.Addresses
	if ips == nil {
		ips = defaultNodeIPs(static, families, subnets)
	}
	var seen []nodeip.Family
	var addrs []netip.Addr
	// The schema's format ip has checked the Node's addresses, and defaultNodeIPs takes the static
	// addresses that parse alone.
	for _, a := range ips {
		if ip, err := netip.ParseAddr(a); err == nil {
			seen = append(seen, nodeip.FamilyOf(ip))
			addrs = append(addrs, ip)
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
			add("Node: nodeIP.addresses: an %s address, but the cluster's ipFamilies are [ %s ]", f, strs.Join(families, " "))
		}
	}
	// The other nodes take VXLAN only from vxlanSourceSubnets, so every address the node has or
	// may pick must lie in them; the node checks the addresses it picks at boot again.
	sources := c.VXLANSources()
	if err := kubernetes.CheckVXLANSources(sources, addrs); err != nil {
		add("Node: nodeIP.addresses: %v", err)
	}
	for _, s := range subnets {
		p, err := netip.ParsePrefix(s)
		if err == nil && !kubernetes.InVXLANSources(sources, p) {
			add("Node: the node picks its address from %s, which is not inside vxlanSourceSubnets, from which alone the other nodes take VXLAN", s)
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
	return ips, errs
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
