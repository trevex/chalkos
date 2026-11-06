package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/router/derive"
)

// The units of the VRF documents, which the output networkd renders on router images beside the
// networkd documents, and VRF= in the Networks of the links each VRF holds, as the model finds
// them.

// renderRouterNetworkd writes the networkd documents of in as renderNetworkd does, with the units
// of the VRF documents: the device's 00-vrf-<name>.netdev, of Kind=vrf and the VRF's table, and
// 00-vrf-<name>.network, which brings the device up, leaves it out of the node's online state
// and ends the table in unreachable default routes, and VRF=<name> in the [Network] section of
// each Network document that is the first to match links the VRF holds, a .network per NAT64
// that leaves its link n64-<name> to chalkos-nat64, and the kernel's rules of the PolicyRoute
// documents and the NextHop documents' tables in the Networks of lo and of the next hops' links.
func renderRouterNetworkd(schemas string, in map[string]any, out string) error {
	d, err := derive.Build(documents(in))
	if err != nil {
		return err
	}
	byKind, _ := in["documents"].(map[string]any)
	docs, err := withVRFUnits(d.VRFs, d.Membership.Networks, byKind)
	if err != nil {
		return err
	}
	if docs, err = withNAT64Units(d, byKind, docs); err != nil {
		return err
	}
	if docs, err = withPolicyRoutes(d, docs); err != nil {
		return err
	}
	return renderNetworkd(schemas, map[string]any{"node": in["node"], "documents": docs}, out)
}

// withVRFUnits is byKind with each VRF's NetDev and Network documents added and VRF= set in each
// Network document that networks puts in a VRF without its own VRF= of it, which only a VRF
// document's interfaces do. It refuses a networkd document of a VRF unit's name and a NetDev
// that creates a device of a VRF's name, by any [NetDev] section and any value of its Name=, a
// check the VRF's rules leave to it.
func withVRFUnits(vrfs []derive.Named[derive.VRFSpec], networks map[string]string, byKind map[string]any) (map[string]any, error) {
	isVRF := func(name string) bool {
		return slices.ContainsFunc(vrfs, func(v derive.Named[derive.VRFSpec]) bool { return v.Name == name })
	}
	for _, kind := range []string{"NetDev", "Network"} {
		docs, _ := byKind[kind].([]any)
		for _, d := range docs {
			doc, _ := d.(map[string]any)
			name, err := derive.DocumentName(kind, doc)
			if err != nil {
				return nil, err
			}
			if v, ok := strings.CutPrefix(name, derive.VRFUnitPrefix); ok && isVRF(v) {
				return nil, refusals.Errorf(kind, name, "metadata.name", "%s %s takes the name of VRF %s's units; rename the document", kind, name, v)
			}
			if kind != "NetDev" {
				continue
			}
			spec, _ := doc["spec"].(map[string]any)
			for si, s := range derive.OneOrMany(spec["netDev"]) {
				section, _ := s.(map[string]any)
				for ni, n := range derive.OneOrMany(section["name"]) {
					if device, _ := n.(string); isVRF(device) {
						at := derive.ItemPath(derive.ItemPath("spec.netDev", spec["netDev"], si)+".name", section["name"], ni)
						return nil, refusals.Errorf("NetDev", name, at, "NetDev %s creates the device %s, which VRF %s creates; rename the VRF or the NetDev", name, device, device)
					}
				}
			}
		}
	}
	netdevs, _ := byKind["NetDev"].([]any)
	docs, _ := byKind["Network"].([]any)
	netdevs, docs = slices.Clone(netdevs), slices.Clone(docs)
	for i, d := range docs {
		doc, _ := d.(map[string]any)
		name, _ := derive.DocumentName("Network", doc)
		spec, _ := doc["spec"].(map[string]any)
		own, _, _ := derive.NetworkVRF(name, spec)
		if vrf, ok := networks[name]; ok && own != vrf {
			docs[i] = withNetworkVRF(doc, vrf)
		}
	}
	for _, v := range vrfs {
		unit := map[string]any{"name": derive.VRFUnitPrefix + v.Name}
		netdevs = append(netdevs, map[string]any{"metadata": unit, "spec": map[string]any{
			"netDev": map[string]any{"kind": "vrf", "name": v.Name},
			"vrf":    map[string]any{"table": json.Number(strconv.FormatUint(uint64(v.Spec.Table), 10))},
		}})
		docs = append(docs, map[string]any{"metadata": unit, "spec": map[string]any{
			"match": map[string]any{"name": v.Name},
			"link":  map[string]any{"requiredForOnline": "no"},
			"route": unreachableDefaults(v.Spec.Table),
		}})
	}
	out := maps.Clone(byKind)
	if out == nil {
		out = map[string]any{}
	}
	out["NetDev"], out["Network"] = netdevs, docs
	return out, nil
}

// withNAT64Units is docs, the networkd documents with the VRFs' units, with a Network document
// 00-n64-<name> per NAT64 that leaves the link n64-<name> unmanaged: chalkos-nat64 configures the
// link, and networkd would drop its addresses and routes once a Network of the node matched it. It
// refuses a Network of byKind of such a unit's name or that networkd may pick for the link before
// it, and a NetDev of byKind that creates a device of the link's name.
func withNAT64Units(d derive.Model, byKind, docs map[string]any) (map[string]any, error) {
	if len(d.NAT64) == 0 {
		return docs, nil
	}
	units, err := d.Units()
	if err != nil {
		return nil, err
	}
	networks, _ := docs["Network"].([]any)
	networks = slices.Clone(networks)
	for _, n := range d.NAT64 {
		link, unit := derive.NAT64LinkPrefix+n.Name, nat64UnitPrefix+n.Name
		// The NAT64's rules keep its name to 11 characters, so the link's name is an interface's.
		if !derive.PlainInterface.MatchString(link) {
			return nil, derive.InternalErrorf("NAT64 %s: its link %s is no interface name of 15 characters", n.Name, link)
		}
		existing, _ := byKind["Network"].([]any)
		for _, d := range existing {
			doc, _ := d.(map[string]any)
			if name, _ := derive.DocumentName("Network", doc); name == unit {
				return nil, refusals.Errorf("Network", name, "metadata.name", "Network %s takes the name of NAT64 %s's unit; rename the document", name, n.Name)
			}
		}
		netdevs, _ := byKind["NetDev"].([]any)
		for _, d := range netdevs {
			doc, _ := d.(map[string]any)
			name, _ := derive.DocumentName("NetDev", doc)
			spec, _ := doc["spec"].(map[string]any)
			for si, s := range derive.OneOrMany(spec["netDev"]) {
				section, _ := s.(map[string]any)
				for ni, device := range derive.OneOrMany(section["name"]) {
					if device == link {
						at := derive.ItemPath(derive.ItemPath("spec.netDev", spec["netDev"], si)+".name", section["name"], ni)
						return nil, refusals.Errorf("NetDev", name, at, "NetDev %s creates the device %s, the link of NAT64 %s; give the device another name", name, link, n.Name)
					}
				}
			}
		}
		units = append(units, derive.Unit{Name: unit, Match: derive.NameMatch{Words: []derive.NameWord{{Glob: link, Text: link}}}, Device: true})
		networks = append(networks, map[string]any{"metadata": map[string]any{"name": unit}, "spec": map[string]any{
			"match": map[string]any{"name": link},
			"link":  map[string]any{"unmanaged": "yes"},
		}})
	}
	slices.SortStableFunc(units, func(a, b derive.Unit) int { return strings.Compare(a.Name+".network", b.Name+".network") })
	for _, n := range d.NAT64 {
		link, own := derive.NAT64LinkPrefix+n.Name, nat64UnitPrefix+n.Name
		if u := derive.Candidates(units, link)[0]; u.Name != own {
			if u.Narrowed {
				return nil, refusals.Errorf("Network", u.Name, "metadata.name", "Network %s may match the link %s of NAT64 %s before %s, should its other [Match] keys match it; rename it to sort after %s", u.Name, link, n.Name, own, own)
			}
			return nil, refusals.Errorf("Network", u.Name, "metadata.name", "Network %s matches the link %s of NAT64 %s before %s; rename it to sort after %s", u.Name, link, n.Name, own, own)
		}
	}
	out := maps.Clone(docs)
	out["Network"] = networks
	return out, nil
}

// vrfDefaultMetric is the metric of the unreachable default routes that end a VRF's table. FRR
// reads a kernel route's metric as a distance in its highest byte, so it takes this one for
// distance 255, and a default route of BGP or of a member's Network wins over it.
const vrfDefaultMetric = "4278198272"

// unreachableDefaults are the [Route] sections of a VRF device's .network that end the VRF's table
// in unreachable default routes of both families. A lookup that the VRF's table does not answer
// goes on to the main table otherwise, so the VRF's traffic would follow the default VRF's routes
// and the loose reverse-path filter would accept sources that only the default VRF reaches.
func unreachableDefaults(table uint32) []any {
	t := json.Number(strconv.FormatUint(uint64(table), 10))
	var routes []any
	for _, destination := range []string{"0.0.0.0/0", "::/0"} {
		routes = append(routes, map[string]any{
			"type":        "unreachable",
			"destination": destination,
			"table":       t,
			"metric":      json.Number(vrfDefaultMetric),
		})
	}
	return routes
}

// withNetworkVRF is a copy of the Network document doc that gives its links VRF=vrf: as the last
// VRF= of the last [Network] section that has one, else in its first [Network] section, which
// it gains when it has none.
func withNetworkVRF(doc map[string]any, vrf string) map[string]any {
	spec, _ := doc["spec"].(map[string]any)
	spec = maps.Clone(spec)
	if spec == nil {
		spec = map[string]any{}
	}
	list, isList := spec["network"].([]any)
	sections := slices.Clone(list)
	if !isList || len(sections) == 0 {
		sections = []any{spec["network"]}
		if isList {
			sections = []any{nil}
		}
	}
	at := 0
	for i, s := range sections {
		if section, _ := s.(map[string]any); section["vrf"] != nil {
			at = i
		}
	}
	section, _ := sections[at].(map[string]any)
	section = maps.Clone(section)
	if section == nil {
		section = map[string]any{}
	}
	if old := section["vrf"]; old != nil {
		section["vrf"] = append(slices.Clone(derive.OneOrMany(old)), vrf)
	} else {
		section["vrf"] = vrf
	}
	sections[at] = section
	if isList {
		spec["network"] = sections
	} else {
		spec["network"] = sections[0]
	}
	out := maps.Clone(doc)
	out["spec"] = spec
	return out
}

// checkPeerDevices refuses a peer's interface that is a VRF's device, and a glob of its
// interfaces that matches one, of the VRF devices that Model.Devices finds: bgpd runs no session on
// the device, and the input hook sees it as the iifname of every packet from the VRF's links, so
// router-nftables would open the peer's ports on all of them.
func checkPeerDevices(p derive.Named[derive.PeerSpec], devices []string) error {
	s := p.Spec
	if s.Interface != nil {
		if dev, ok := derive.DeviceMatch(*s.Interface, devices); ok {
			return refusals.Errorf("BGPPeer", p.Name, "spec.interface", "interface %s is the device of VRF %s", dev, dev)
		}
	}
	for i, glob := range s.Interfaces {
		if dev, ok := derive.DeviceMatch(glob, devices); ok {
			return refusals.Errorf("BGPPeer", p.Name, fmt.Sprintf("spec.interfaces[%d]", i), "%s matches %s, the device of VRF %s", glob, dev, dev)
		}
	}
	return nil
}
