package config

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	"github.com/trevex/chalkos/pkg/manifest"
)

// Target is the image a set is translated for.
type Target struct {
	// Role and Platform are the image's, as its os-release names them.
	Role     string
	Platform string
	// Kubernetes is the image's /etc/chalkos/kubernetes/cluster.json; nil on an image without
	// Kubernetes.
	Kubernetes *kubernetes.Cluster
}

// defaultTimeServers are the servers of a set without a TimeSync, the TimeSync kind's default.
var defaultTimeServers = []manifest.TimeServer{
	{Host: "ptbtime1.ptb.de", NTS: true},
	{Host: "ptbtime2.ptb.de", NTS: true},
	{Host: "ptbtime3.ptb.de", NTS: true},
}

// Identity translates the set into the node's identity for the image t describes, as the cluster
// definition rendered it, so every reader of the identity on the node keeps working. Extension
// kinds stay in the set and do not reach the identity's extensions.
func (s *Set) Identity(t Target) (manifest.Identity, error) {
	cluster, _ := s.One("Cluster")
	nodeDoc, _ := s.One("Node")
	var node nodeSpec
	if err := nodeDoc.decodeSpec(&node); err != nil {
		return manifest.Identity{}, err
	}
	if err := checkType(nodeDoc, node, t); err != nil {
		return manifest.Identity{}, err
	}
	sec, err := s.storage()
	if err != nil {
		return manifest.Identity{}, err
	}
	var networkDocs []Document
	for _, kind := range []string{"Link", "NetDev", "Network"} {
		networkDocs = append(networkDocs, s.Of(NetworkdAPI, kind)...)
	}
	units, err := renderNetworkUnits(networkDocs, s.schemas)
	if err != nil {
		return manifest.Identity{}, err
	}
	id := manifest.Identity{
		Hostname:     nodeDoc.Name,
		Cluster:      cluster.Name,
		Role:         t.Role,
		Platform:     t.Platform,
		Network:      NetworkMap(networkDocs),
		NetworkUnits: units,
		Labels:       map[string]string{},
		Taints:       []manifest.Taint{},
		Storage:      sec,
		Time:         manifest.Time{Servers: slices.Clone(defaultTimeServers)},
		// Empty rather than nil, so the identity marshals with "extensions":{} as the cluster
		// definition's does.
		Extensions: map[string]json.RawMessage{},
	}
	if node.Hostname != nil {
		id.Hostname = *node.Hostname
	}
	if ts, ok := s.One("TimeSync"); ok {
		var spec timeSyncSpec
		if err := ts.decodeSpec(&spec); err != nil {
			return manifest.Identity{}, err
		}
		id.Time.Servers = []manifest.TimeServer{}
		for _, srv := range spec.Servers {
			server := manifest.TimeServer{Host: srv.Host, NTS: srv.NTS}
			if srv.Port != nil {
				server.Port = *srv.Port
			}
			id.Time.Servers = append(id.Time.Servers, server)
		}
	}
	if t.Kubernetes != nil {
		if err := s.kubelet(&id, nodeDoc.Name, *t.Kubernetes); err != nil {
			return manifest.Identity{}, err
		}
	}
	return id, nil
}

// checkType requires the Node's type to be the kind of node its image is built for, and none on
// an image without Kubernetes.
func checkType(d Document, node nodeSpec, t Target) error {
	switch {
	case t.Kubernetes == nil && node.Type != nil:
		return fmt.Errorf("%s has type %s, but its image runs no Kubernetes", d, *node.Type)
	case t.Kubernetes != nil && node.Type == nil:
		return fmt.Errorf("%s has no type, but its image is built for %s nodes; set type %s", d, t.Kubernetes.Kind, t.Kubernetes.Kind)
	case t.Kubernetes != nil && *node.Type != t.Kubernetes.Kind:
		return fmt.Errorf("%s has type %s, but its image is built for %s nodes", d, *node.Type, t.Kubernetes.Kind)
	}
	return nil
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

// kubelet fills in the node's Kubernetes settings from its Kubelet document, or the kind's
// defaults without one, and refuses settings the kubelet would not start with. The errors come
// in the cluster definition's order: refused labels and swap volumes by name, then the node's
// addresses.
func (s *Set) kubelet(id *manifest.Identity, name string, c kubernetes.Cluster) error {
	var spec kubeletSpec
	if d, ok := s.One("Kubelet"); ok {
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
			add("Kubelet: label %q: the kubelet sets labels in the kubernetes.io and k8s.io namespaces on its own node only under kubelet.kubernetes.io/ and node.kubernetes.io/ or from the list %s", key, strings.Join(kubeletLabels, ", "))
		}
	}
	for _, t := range spec.Taints {
		id.Taints = append(id.Taints, manifest.Taint{Key: t.Key, Value: deref(t.Value), Effect: t.Effect})
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
	if spec.ValidSubnets != nil {
		subnets = spec.ValidSubnets
	}
	_, subnetErr := nodeip.ParseFilter(spec.ValidSubnets)
	if subnetErr != nil {
		add("Kubelet: %v", subnetErr)
	}
	nodeIPs := spec.NodeIPs
	if nodeIPs == nil {
		nodeIPs = defaultNodeIPs(id.StaticAddresses(), families, subnets)
	}
	var seen []nodeip.Family
	for _, a := range nodeIPs {
		if ip, err := netip.ParseAddr(a); err == nil {
			seen = append(seen, nodeip.FamilyOf(ip))
		} else {
			add("Kubelet: nodeIPs: %q is not an address", a)
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
			add("Kubelet: nodeIPs: more than one %s address; give at most one per family", f)
		}
	}
	for _, f := range unique {
		if !slices.Contains(families, f) {
			add("Kubelet: nodeIPs: an %s address, but the cluster's ipFamilies are [ %s ]", f, joinFamilies(families))
		}
	}
	// Invalid subnets are reported above; which families they would pick from is moot.
	if subnetErr == nil {
		for _, f := range families {
			if !slices.Contains(seen, f) && !picks(subnets, f) {
				add("Kubelet: no %s address in nodeIPs, and no %s subnet in the validSubnets that apply, so the node cannot pick its %s address", f, f, f)
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid Kubernetes settings:\n- %s", strings.Join(errs, "\n- "))
	}
	id.Kubernetes = &manifest.KubernetesIdentity{NodeName: name, NodeIPs: nodeIPs, ValidSubnets: spec.ValidSubnets}
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
