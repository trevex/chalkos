package main

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"

	"github.com/trevex/chalkos/pkg/router/derive"
)

// nat64UnitPrefix starts the name of the files of a NAT64 link's .network of networkd,
// 00-n64-<name>.
const nat64UnitPrefix = "00-n64-"

// ownPorts are the ports of the inner address that a NAT64 of an empty pool4 takes, those Jool
// takes of a node's own addresses; the firewall masquerades them within the same range.
const ownPorts = "61001-65535"

// joolInstance is a NAT64 document as jool file handle reads it.
type joolInstance struct {
	Instance  string      `json:"instance"`
	Framework string      `json:"framework"`
	Global    joolGlobal  `json:"global"`
	Pool4     []joolPool4 `json:"pool4"`
}

type joolGlobal struct {
	Pool6 string `json:"pool6"`
}

type joolPool4 struct {
	Protocol  string `json:"protocol"`
	Prefix    string `json:"prefix"`
	PortRange string `json:"port range"`
}

// renderJool writes, for each NAT64 document, Jool's file to out/<name>.json, an instance of the
// document's name in netfilter mode with the document's prefix as its pool6 and its pool as its
// pool4, an entry per protocol, and its link's file to out/<name>.veth, which chalkos-nat64.service
// reads to join the instance's namespace to its VRF. A node without NAT64 documents gets no file
// and runs no instance.
func renderJool(in map[string]any, out string) error {
	d, err := derive.Build(documents(in))
	if err != nil {
		return err
	}
	instances, err := d.NAT64Instances()
	if err != nil {
		return err
	}
	for _, n := range instances {
		for _, f := range []struct {
			name string
			v    any
		}{{n.Name + ".json", joolOf(n)}, {n.Name + ".veth", linkOf(n)}} {
			data, err := json.MarshalIndent(f.v, "", "  ")
			if err != nil {
				return err
			}
			if err := writeFile(out, f.name, append(data, '\n')); err != nil {
				return err
			}
		}
	}
	return nil
}

// joolOf is Jool's instance of the NAT64 n: an entry of its pool4 per protocol of each pool
// entry, in Jool's order, and for an empty pool4 an entry per protocol of the link's inner
// address with the ports ownPorts, which the firewall masquerades.
func joolOf(n derive.NAT64Instance) joolInstance {
	inst := joolInstance{Instance: n.Name, Framework: "netfilter", Global: joolGlobal{Pool6: n.Prefix.String()}, Pool4: []joolPool4{}}
	for _, e := range n.Pool4 {
		for _, proto := range e.Protocols {
			inst.Pool4 = append(inst.Pool4, joolPool4{Protocol: strings.ToUpper(proto), Prefix: e.Prefix.String(), PortRange: fmt.Sprintf("%d-%d", e.First, e.Last)})
		}
	}
	if len(n.Pool4) == 0 {
		own := netip.PrefixFrom(n.Inner.Addr(), 32).String()
		for _, proto := range derive.PoolProtocols {
			inst.Pool4 = append(inst.Pool4, joolPool4{Protocol: strings.ToUpper(proto), Prefix: own, PortRange: ownPorts})
		}
	}
	return inst
}

// linkOf is the file of the NAT64 n's link, which names the default VRF by null, as chalkos-nat64
// reads it.
func linkOf(n derive.NAT64Instance) nat64Link {
	var vrf *string
	if n.VRF != derive.DefaultVRF {
		vrf = &n.VRF
	}
	routes := []string{}
	for _, p := range n.Pools {
		routes = append(routes, p.String())
	}
	return nat64Link{VRF: vrf, Table: n.Table, Outer: n.Outer.String(), Inner: n.Inner.String(), Routes: routes}
}

// nat64Link is a NAT64's file <name>.veth: the VRF of its link n64-<name>, null for the default
// VRF, and that VRF's table; the addresses of the link's outer end and of its inner end in the
// namespace; and the IPv4 prefixes routed to the inner end, those of an explicit pool4.
type nat64Link struct {
	VRF    *string  `json:"vrf"`
	Table  uint32   `json:"table"`
	Outer  string   `json:"outer"`
	Inner  string   `json:"inner"`
	Routes []string `json:"routes"`
}
