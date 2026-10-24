package kubernetes

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
)

// validateRanges checks the ranges of family f as kubeadm does:
//   - each range is a network of its family;
//   - the pod and service ranges are apart;
//   - the service range holds at most 2^20 addresses;
//   - the node parts of the pod range are longer than it by at most 16 bits;
//   - the DNS address lies inside the service range, but is neither its network address nor its
//     first address, which the kubernetes service gets.
func (c Cluster) validateRanges(f nodeip.Family) error {
	pod := c.PodCIDRs.Of(f)
	if err := checkRange("podCIDRs", f, pod); err != nil {
		return err
	}
	service := c.ServiceCIDRs.Of(f)
	if err := checkRange("serviceCIDRs", f, service); err != nil {
		return err
	}
	if pod.Overlaps(service) {
		return fmt.Errorf("podCIDRs.%s %s and serviceCIDRs.%s %s overlap", f, pod, f, service)
	}
	bits := service.Addr().BitLen()
	if bits-service.Bits() > 20 {
		return fmt.Errorf("serviceCIDRs.%s %s holds more than 2^20 addresses; use a prefix of /%d or longer", f, service, bits-20)
	}
	mask := c.NodeCIDRMaskSizes.Of(f)
	if mask > bits {
		return fmt.Errorf("nodeCIDRMaskSizes.%s %d is longer than an %s address", f, mask, f)
	}
	if mask <= pod.Bits() || mask-pod.Bits() > 16 {
		return fmt.Errorf("nodeCIDRMaskSizes.%s %d must be longer than the prefix of podCIDRs.%s %s, by at most 16 bits", f, mask, f, pod)
	}
	dns := c.DNSIPs.Of(f)
	if !dns.IsValid() || dns.Zone() != "" || !service.Contains(dns) {
		return fmt.Errorf("dnsIPs.%s %q is not an address in serviceCIDRs.%s %s", f, dns, f, service)
	}
	if dns == service.Addr() || dns == service.Addr().Next() {
		return fmt.Errorf("dnsIPs.%s %s is the network address of serviceCIDRs.%s %s or the kubernetes service's address", f, dns, f, service)
	}
	return nil
}

// checkRange checks the range of family f that option holds: a network of that family, without
// host bits.
func checkRange(option string, f nodeip.Family, p netip.Prefix) error {
	if !p.IsValid() {
		return fmt.Errorf("%s.%s is not set", option, f)
	}
	if p.Addr().Is4In6() {
		return fmt.Errorf("%s.%s %s is an IPv4-mapped IPv6 range; write the IPv4 form", option, f, p)
	}
	if p.Addr().Is4() != (f == nodeip.IPv4) {
		return fmt.Errorf("%s.%s %s is not an %s range", option, f, p, f)
	}
	if p != p.Masked() {
		return fmt.Errorf("%s.%s %s has host bits set; write %s", option, f, p, p.Masked())
	}
	return nil
}

// validateVXLANSources checks the ranges flannel's VXLAN must come from: networks of the
// cluster's families, at least one of each or none, and none overlapping the pod or service
// ranges, from which pods could pass the source check.
func (c Cluster) validateVXLANSources(families []nodeip.Family) error {
	sources := c.VXLANSources()
	have := map[nodeip.Family]bool{}
	for _, p := range sources {
		f := nodeip.FamilyOf(p.Addr())
		if err := checkRange("vxlanSourceSubnets", f, p); err != nil {
			return err
		}
		if !slices.Contains(families, f) {
			return fmt.Errorf("vxlanSourceSubnets: %s is an %s range, but the cluster's families are %v", p, f, families)
		}
		have[f] = true
	}
	for _, f := range families {
		if len(sources) > 0 && !have[f] {
			return fmt.Errorf("vxlanSourceSubnets: no %s range, so no node would take %s VXLAN", f, f)
		}
	}
	for _, s := range sources {
		f := nodeip.FamilyOf(s.Addr())
		for _, r := range []struct {
			option string
			p      netip.Prefix
		}{{"podCIDRs", c.PodCIDRs.Of(f)}, {"serviceCIDRs", c.ServiceCIDRs.Of(f)}} {
			if s.Overlaps(r.p) {
				return fmt.Errorf("vxlanSourceSubnets: %s overlaps %s.%s %s", s, r.option, f, r.p)
			}
		}
	}
	return nil
}

// VXLANSources returns the ranges flannel's VXLAN must come from: networks of the cluster's
// families, at least one of each, or none, as without flannel.
func (c Cluster) VXLANSources() []netip.Prefix {
	if c.Flannel == nil || len(c.Flannel.VXLANSourceSubnets) == 0 {
		return nil
	}
	return slices.Clone(c.Flannel.VXLANSourceSubnets)
}

// CheckVXLANSources refuses node addresses outside the ranges the other nodes take VXLAN from,
// sources from VXLANSources: their pods could not reach this node's. No sources take VXLAN from
// anywhere.
func CheckVXLANSources(sources []netip.Prefix, addrs []netip.Addr) error {
	for _, a := range addrs {
		if !InVXLANSources(sources, netip.PrefixFrom(a, a.BitLen())) {
			return fmt.Errorf("the node's address %s is outside vxlanSourceSubnets %s, from which alone the other nodes take VXLAN", a, joinPrefixes(sources))
		}
	}
	return nil
}

// InVXLANSources tells whether every address of the range p lies in one of sources, or sources
// are none.
func InVXLANSources(sources []netip.Prefix, p netip.Prefix) bool {
	return len(sources) == 0 || slices.ContainsFunc(sources, func(s netip.Prefix) bool {
		return s.Bits() <= p.Bits() && s.Contains(p.Masked().Addr())
	})
}

// joinPrefixes lists the ranges, separated by commas.
func joinPrefixes(prefixes []netip.Prefix) string {
	list := make([]string, len(prefixes))
	for i, p := range prefixes {
		list[i] = p.String()
	}
	return strings.Join(list, ", ")
}

// PodCIDRList returns the pod ranges of the cluster's families, the primary family's first.
func (c Cluster) PodCIDRList() []netip.Prefix {
	return c.rangeList(c.PodCIDRs)
}

// ServiceCIDRList returns the service ranges of the cluster's families, the primary family's
// first.
func (c Cluster) ServiceCIDRList() []netip.Prefix {
	return c.rangeList(c.ServiceCIDRs)
}

func (c Cluster) rangeList(ranges ByFamily[netip.Prefix]) []netip.Prefix {
	var out []netip.Prefix
	for _, f := range c.Families() {
		out = append(out, ranges.Of(f))
	}
	return out
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
