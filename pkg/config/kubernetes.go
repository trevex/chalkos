package config

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"

	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
)

// Cluster builds the Kubernetes cluster the documents give on an image with the facts img:
// its kind, the kubelet's extra flags and the VIP's interface from the Kubernetes Node, its
// endpoint and other settings from the Cluster document and, on a flannel image, flannel's from
// the Flannel document, and checks it. It holds the per-family values of the cluster's families
// alone, and each error names the document whose setting fails. docs carry their schema
// defaults, as a Set and the stored config do.
func (docs Documents) Cluster(img kubernetes.Image) (kubernetes.Cluster, error) {
	nodeDoc, ok := docs.single(KubernetesAPI, "Node")
	if !ok {
		return kubernetes.Cluster{}, errors.New("no Node: the config of a node whose image runs Kubernetes holds one Node document")
	}
	var node nodeSpec
	if err := nodeDoc.decodeSpec(&node); err != nil {
		return kubernetes.Cluster{}, err
	}
	kdoc, ok := docs.single(KubernetesAPI, "Cluster")
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
		NodeIP:                         k.NodeIP,
		VIP:                            kubernetes.VIP{Mode: k.VIP.Mode, Interface: deref(node.VIP.Interface)},
		ExtraArgs:                      maps.Clone(k.ExtraArgs),
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
	k.setFamilies(&c)
	if err := c.Validate(); err != nil {
		return kubernetes.Cluster{}, fmt.Errorf("%s: %w", kdoc, err)
	}
	if img.CNI == kubernetes.CNIFlannel {
		c.Flannel = &kubernetes.Flannel{VXLANSourceSubnets: []netip.Prefix{}}
		if d, ok := docs.single(KubernetesAPI, "Flannel"); ok {
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

// setFamilies sets the cluster's families, the ranges and DNS addresses of these families and the
// VIPs from the spec. A cluster file holds the per-family values of the families in use alone, so
// the values of the other families stay unset; Cluster.Validate checks those in use.
func (k clusterSpec) setFamilies(c *kubernetes.Cluster) {
	c.IPFamilies = k.IPFamilies
	for _, f := range c.Families() {
		service := k.ServiceCIDRs.Of(f)
		dns := dnsIP(k.DNSIPs.Of(f), service)
		if f == nodeip.IPv4 {
			c.PodCIDRs.IPv4, c.ServiceCIDRs.IPv4, c.DNSIPs.IPv4 = k.PodCIDRs.IPv4, service, dns
			c.NodeCIDRMaskSizes.IPv4 = k.NodeCIDRMaskSizes.IPv4
		} else {
			c.PodCIDRs.IPv6, c.ServiceCIDRs.IPv6, c.DNSIPs.IPv6 = k.PodCIDRs.IPv6, service, dns
			c.NodeCIDRMaskSizes.IPv6 = k.NodeCIDRMaskSizes.IPv6
		}
	}
	c.VIP.Addresses = k.VIP.Addresses
}

// dnsIP is the DNS address given, or the 10th address of the service range, which kubeadm gives
// the cluster DNS.
func dnsIP(given *netip.Addr, service netip.Prefix) netip.Addr {
	if given != nil {
		return *given
	}
	a := service.Masked().Addr()
	for range 10 {
		a = a.Next()
	}
	return a
}

// AddonObjects returns the objects of the Addon documents, in the order of the documents' names
// and each document's own order: the objects the control planes apply after chalkos's.
func (docs Documents) AddonObjects() ([]map[string]any, error) {
	addons := docs.Of(KubernetesAPI, "Addon")
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

// CheckKubernetes refuses a set whose Node, Cluster, Flannel and Addon documents a node of an
// image with the facts img could not run on: a cluster the Node, Cluster and Flannel documents do
// not build, and Addon documents that do not decode.
func (s *Set) CheckKubernetes(img kubernetes.Image) error {
	if _, err := s.docs.Cluster(img); err != nil {
		return err
	}
	_, err := s.docs.AddonObjects()
	return err
}
