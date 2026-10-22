package config

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
)

// docsOf returns the documents of a kind among docs.
func docsOf(docs []Document, apiVersion, kind string) []Document {
	var out []Document
	for _, d := range docs {
		if d.APIVersion == apiVersion && d.Kind == kind {
			out = append(out, d)
		}
	}
	return out
}

// single returns the document of a kind when docs hold exactly one.
func single(docs []Document, apiVersion, kind string) (Document, bool) {
	if found := docsOf(docs, apiVersion, kind); len(found) == 1 {
		return found[0], true
	}
	return Document{}, false
}

// ClusterOf builds the Kubernetes cluster a node's documents give on an image with the facts img:
// its kind, the kubelet's extra flags and the VIP's interface from the Kubernetes Node, its
// endpoint and other settings from the Cluster document and, on a flannel image, flannel's from
// the Flannel document, and checks it. It holds the per-family values of the cluster's families
// alone, and each error names the document whose setting fails. docs carry their schema
// defaults, as a Set and the stored config do.
func ClusterOf(docs []Document, img kubernetes.Image) (kubernetes.Cluster, error) {
	nodeDoc, ok := single(docs, KubernetesAPI, "Node")
	if !ok {
		return kubernetes.Cluster{}, errors.New("no Node: the config of a node whose image runs Kubernetes holds one Node document")
	}
	var node nodeSpec
	if err := nodeDoc.decodeSpec(&node); err != nil {
		return kubernetes.Cluster{}, err
	}
	kdoc, ok := single(docs, KubernetesAPI, "Cluster")
	if !ok {
		return kubernetes.Cluster{}, errors.New("no Cluster: the config of a node whose image runs Kubernetes holds one Cluster document")
	}
	var k clusterSpec
	if err := kdoc.decodeSpec(&k); err != nil {
		return kubernetes.Cluster{}, err
	}
	c := kubernetes.Cluster{
		Kind:                           node.Type,
		Endpoint:                       k.Endpoint,
		Version:                        img.Version,
		Images:                         img.Images,
		Domain:                         k.Domain,
		AllowSchedulingOnControlPlanes: k.AllowSchedulingOnControlPlanes,
		IPFamilies:                     k.IPFamilies,
		PodCIDRs:                       k.PodCIDRs,
		ServiceCIDRs:                   k.ServiceCIDRs,
		DNSIPs: kubernetes.ByFamily[string]{
			IPv4: dnsIP(k.DNSIPs.IPv4, k.ServiceCIDRs.IPv4),
			IPv6: dnsIP(k.DNSIPs.IPv6, k.ServiceCIDRs.IPv6),
		},
		NodeCIDRMaskSizes: k.NodeCIDRMaskSizes,
		NodeIP:            k.NodeIP,
		VIP:               kubernetes.VIP{Addresses: k.VIP.Addresses, Mode: k.VIP.Mode, Interface: deref(node.VIP.Interface)},
		ExtraArgs:         maps.Clone(k.ExtraArgs),
	}
	// The kubelet's flags are the node's own, which the Node document names.
	if err := kubernetes.ValidateFlags("kubelet", node.KubeletExtraArgs); err != nil {
		return kubernetes.Cluster{}, fmt.Errorf("%s: kubeletExtraArgs: %w", nodeDoc, err)
	}
	if c.ExtraArgs == nil {
		c.ExtraArgs = map[string]map[string]string{}
	}
	c.ExtraArgs["kubelet"] = node.KubeletExtraArgs
	// Only control planes hold the VIP, so an interface for it on a worker is a mistake.
	if node.Type == kubernetes.KindWorker && node.VIP.Interface != nil {
		return kubernetes.Cluster{}, fmt.Errorf("%s: vip.interface: only control planes hold the VIP; leave it null on a worker", nodeDoc)
	}
	if _, err := c.EndpointHost(); err != nil {
		return kubernetes.Cluster{}, fmt.Errorf("%s: %w", kdoc, err)
	}
	if err := c.Validate(); err != nil {
		return kubernetes.Cluster{}, fmt.Errorf("%s: %w", kdoc, err)
	}
	keepFamilies(&c)
	if img.CNI == kubernetes.CNIFlannel {
		c.Flannel = &kubernetes.Flannel{VXLANSourceSubnets: []string{}}
		if d, ok := single(docs, KubernetesAPI, "Flannel"); ok {
			var f flannelSpec
			if err := d.decodeSpec(&f); err != nil {
				return kubernetes.Cluster{}, err
			}
			if f.MTU != nil {
				c.Flannel.MTU = *f.MTU
			}
			c.Flannel.VXLANSourceSubnets = f.VXLANSourceSubnets
			// The cluster without flannel is valid by now, so what fails is the Flannel
			// document's MTU or VXLAN sources, checked against the Cluster document's families
			// and ranges.
			if err := c.Validate(); err != nil {
				return kubernetes.Cluster{}, fmt.Errorf("%s: %w", d, err)
			}
		}
	}
	return c, nil
}

// keepFamilies clears the per-family values of the families the cluster does not use, so a cluster
// file holds the settings that apply alone.
func keepFamilies(c *kubernetes.Cluster) {
	families, err := c.Families()
	if err != nil {
		return
	}
	for _, f := range []nodeip.Family{nodeip.IPv4, nodeip.IPv6} {
		if slices.Contains(families, f) {
			continue
		}
		if f == nodeip.IPv4 {
			c.PodCIDRs.IPv4, c.ServiceCIDRs.IPv4, c.DNSIPs.IPv4, c.NodeCIDRMaskSizes.IPv4 = "", "", "", 0
		} else {
			c.PodCIDRs.IPv6, c.ServiceCIDRs.IPv6, c.DNSIPs.IPv6, c.NodeCIDRMaskSizes.IPv6 = "", "", "", 0
		}
	}
}

// dnsIP is the DNS address given, or the 10th address of the service range, which kubeadm gives
// the cluster DNS; "" for a range that does not parse, which Validate reports.
func dnsIP(given *string, service string) string {
	if given != nil {
		return *given
	}
	p, err := netip.ParsePrefix(service)
	if err != nil {
		return ""
	}
	a := p.Masked().Addr()
	for range 10 {
		a = a.Next()
	}
	return a.String()
}

// Cluster builds the set's Kubernetes cluster on an image with the facts img.
func (s *Set) Cluster(img kubernetes.Image) (kubernetes.Cluster, error) {
	return ClusterOf(s.docs, img)
}

// Registry is what a RegistryMirror says of its registry.
type Registry struct {
	Mirrors        []string
	AllowPlainHTTP bool
}

// RegistryMirrorsOf returns the mirrors of each registry the documents name. A registry named by
// two documents and an http:// mirror without allowPlainHTTP are refused.
func RegistryMirrorsOf(docs []Document) (map[string]Registry, error) {
	out := map[string]Registry{}
	by := map[string]Document{}
	for _, d := range docsOf(docs, KubernetesAPI, "RegistryMirror") {
		var spec registryMirrorSpec
		if err := d.decodeSpec(&spec); err != nil {
			return nil, err
		}
		if other, ok := by[spec.Registry]; ok {
			return nil, fmt.Errorf("%s and %s both mirror %s; give one document per registry", other, d, spec.Registry)
		}
		by[spec.Registry] = d
		for _, m := range spec.Mirrors {
			if !strings.HasPrefix(m, "https://") && !spec.AllowPlainHTTP {
				return nil, fmt.Errorf("%s: the mirror %s is not an https:// URL; set allowPlainHTTP for a test registry, since nothing authenticates what a plain HTTP mirror serves", d, m)
			}
		}
		out[spec.Registry] = Registry{Mirrors: spec.Mirrors, AllowPlainHTTP: spec.AllowPlainHTTP}
	}
	return out, nil
}

// RegistryMirrors returns the set's registry mirrors by registry.
func (s *Set) RegistryMirrors() (map[string]Registry, error) { return RegistryMirrorsOf(s.docs) }

// AddonObjectsOf returns the objects of the Addon documents, in the order of the documents' names
// and each document's own order: the objects the control planes apply after chalkos's.
func AddonObjectsOf(docs []Document) ([]map[string]any, error) {
	addons := docsOf(docs, KubernetesAPI, "Addon")
	slices.SortStableFunc(addons, func(a, b Document) int { return cmp.Compare(a.Name, b.Name) })
	var out []map[string]any
	for _, d := range addons {
		var spec addonSpec
		if err := d.decodeSpec(&spec); err != nil {
			return nil, err
		}
		out = append(out, spec.Objects...)
	}
	return out, nil
}

// AddonObjects returns the objects of the set's Addon documents in the order they apply.
func (s *Set) AddonObjects() ([]map[string]any, error) { return AddonObjectsOf(s.docs) }

// TimeDHCP tells whether the node also takes the time servers DHCP announces.
func (s *Set) TimeDHCP() (bool, error) {
	d, ok := s.One("TimeSync")
	if !ok {
		return false, nil
	}
	var spec timeSyncSpec
	err := d.decodeSpec(&spec)
	return spec.DHCP, err
}

// CheckKubernetes refuses a set whose Node, Cluster, Flannel, RegistryMirror and Addon documents
// a node of an image with the facts img could not run on: a cluster the Node, Cluster and Flannel
// documents do not build, registry mirrors that conflict or are plain HTTP without leave, and
// Addon documents that do not decode.
func (s *Set) CheckKubernetes(img kubernetes.Image) error {
	if _, err := s.Cluster(img); err != nil {
		return err
	}
	if _, err := s.RegistryMirrors(); err != nil {
		return err
	}
	_, err := s.AddonObjects()
	return err
}
