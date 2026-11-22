package translate

import (
	"net/netip"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/kubernetes"
)

// OpenVXLAN reports whether the nodes run flannel and its Flannel document gives no
// vxlanSourceSubnets, so that every node takes VXLAN from any source; documents gives a node's
// documents. The nodes hold alike Cluster and Flannel documents, which Endpoint checks, and a
// Cluster document names flannel's image on a flannel image alone. suggested are the prefixes
// that cover the addresses the nodes register with, in address order: the network prefix of a
// static address, the address alone when no static network holds it, and the subnets a node picks
// its address from at boot. Nodes whose documents do not read are skipped.
func OpenVXLAN(nodes []string, documents func(name string) ([]config.Document, error)) (open bool, suggested []netip.Prefix) {
	for _, n := range nodes {
		list, err := documents(n)
		if err != nil {
			continue
		}
		docs := config.Documents(list)
		var k clusterSpec
		kdoc, ok := docs.Single(API, "Cluster")
		if !ok || kdoc.DecodeSpec(&k) != nil || k.Images.Flannel == "" {
			continue
		}
		var f flannelSpec
		if d, ok := docs.Single(API, "Flannel"); ok && (d.DecodeSpec(&f) != nil || len(f.VXLANSourceSubnets) > 0) {
			return false, nil
		}
		open = true
		for _, p := range nodePrefixes(docs, k) {
			if !slices.Contains(suggested, p) {
				suggested = append(suggested, p)
			}
		}
	}
	slices.SortFunc(suggested, func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	})
	return open, suggested
}

// nodePrefixes are the prefixes that cover the addresses the node of docs registers with in the
// cluster of spec k, as kubelet picks them.
func nodePrefixes(docs config.Documents, k clusterSpec) []netip.Prefix {
	var node nodeSpec
	if d, ok := docs.Single(API, "Node"); ok && d.DecodeSpec(&node) != nil {
		return nil
	}
	id := identity.Identity{Network: config.NetworkMap(docs)}
	static := id.StaticPrefixes()
	subnets := k.NodeIP.ValidSubnets
	if node.NodeIP.ValidSubnets != nil {
		subnets = node.NodeIP.ValidSubnets
	}
	ips := node.NodeIP.Addresses
	if ips == nil {
		if len(subnets) > 0 {
			var out []netip.Prefix
			for _, s := range subnets {
				if p, err := netip.ParsePrefix(s); err == nil && !strings.HasPrefix(s, "!") {
					out = append(out, p.Masked())
				}
			}
			return out
		}
		ips = defaultNodeIPs(id.StaticAddresses(), kubernetes.Cluster{IPFamilies: k.IPFamilies}.Families(), nil)
	}
	var out []netip.Prefix
	for _, a := range ips {
		ip, err := netip.ParseAddr(a)
		if err != nil {
			continue
		}
		p := netip.PrefixFrom(ip, ip.BitLen())
		if i := slices.IndexFunc(static, func(s netip.Prefix) bool { return s.Addr() == ip }); i >= 0 {
			p = static[i].Masked()
		}
		out = append(out, p)
	}
	return out
}
