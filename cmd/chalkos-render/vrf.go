package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// The VRF documents: each link's VRF, which the outputs frr and router-nftables read, and the
// units of the VRFs, which the output networkd renders on router images beside the networkd
// documents.

// defaultVRF names the VRF of every link no VRF holds, as FRR and the router kinds name it.
const defaultVRF = "default"

// vrfUnitPrefix starts the names of a VRF device's units, so that its .network sorts before the
// node's own and networkd configures the device with it.
const vrfUnitPrefix = "00-vrf-"

// vrfLinks checks the VRF documents against the Network documents of byKind, as their rules do
// and where the rules cannot, and returns the VRF of each link a Network document names: the VRF
// whose document names the link or a glob that matches it, or the one its Network document gives
// with VRF= itself. A link in no VRF is not in the map. A VRF's literal interface is a link a
// Network document names, since VRF= reaches a link through its Network alone, and the links of
// one Network document are in one VRF, or in none.
func vrfLinks(vrfs []named[vrfSpec], byKind map[string]any) (map[string]string, error) {
	links := networkLinks(byKind)
	member := map[string]string{}
	tables := map[uint32]string{}
	for _, v := range vrfs {
		what := "VRF " + v.Name
		if v.Name == defaultVRF || len(v.Name) > 15 {
			return nil, fmt.Errorf("%s: a VRF is named as its device, in 15 characters at most, and not %s", what, defaultVRF)
		}
		if slices.Contains(links, v.Name) {
			return nil, fmt.Errorf("%s: a Network document names the link %s, the VRF's device", what, v.Name)
		}
		if t := v.Spec.Table; t == 0 || t >= 253 && t <= 255 {
			return nil, fmt.Errorf("%s: table %d is no table of a VRF; 0, 253, 254 and 255 are the kernel's", what, t)
		}
		if other, ok := tables[v.Spec.Table]; ok {
			return nil, fmt.Errorf("VRF %s and VRF %s both use table %d", other, v.Name, v.Spec.Table)
		}
		tables[v.Spec.Table] = v.Name
		for _, i := range v.Spec.Interfaces {
			if !interfaceName.MatchString(i) {
				return nil, fmt.Errorf("%s: %q is no interface name or glob", what, i)
			}
			held := []string{i}
			if strings.HasSuffix(i, "*") {
				held = slices.DeleteFunc(slices.Clone(links), func(l string) bool { return !matchesGlob(i, l) })
			} else if !slices.Contains(links, i) {
				return nil, fmt.Errorf("%s names the interface %s, which no Network document's [Match] Name= names; VRF= reaches a link through its Network document, so give it one", what, i)
			}
			for _, l := range held {
				if other, ok := member[l]; ok && other != v.Name {
					return nil, fmt.Errorf("the link %s is in VRF %s and in VRF %s", l, other, v.Name)
				}
				member[l] = v.Name
			}
		}
	}
	docs, _ := byKind["Network"].([]any)
	for _, d := range docs {
		doc, _ := d.(map[string]any)
		name, err := documentName("Network", doc)
		if err != nil {
			return nil, err
		}
		spec, _ := doc["spec"].(map[string]any)
		own, err := networkVRF(name, spec)
		if err != nil {
			return nil, err
		}
		names := matchLinks(spec)
		var in []string
		for _, l := range names {
			in = append(in, member[l])
		}
		slices.Sort(in)
		in = slices.Compact(in)
		switch {
		case len(in) > 1:
			return nil, fmt.Errorf("Network %s matches links of %s; the links of one Network are in one VRF", name, vrfList(in))
		case len(in) == 1 && in[0] != "":
			if own != "" && own != in[0] {
				return nil, fmt.Errorf("Network %s gives its links VRF=%s, but VRF %s holds them", name, own, in[0])
			}
		case own != "":
			for _, l := range names {
				member[l] = own
			}
		}
	}
	return member, nil
}

// vrfList names VRFs for a message, "" being no VRF.
func vrfList(vrfs []string) string {
	names := make([]string, len(vrfs))
	for i, v := range vrfs {
		names[i] = "VRF " + v
		if v == "" {
			names[i] = "no VRF"
		}
	}
	return strings.Join(names, " and ")
}

// networkVRF is the VRF= a Network document's [Network] sections give, "" for none.
func networkVRF(name string, spec map[string]any) (string, error) {
	vrf := ""
	for _, s := range oneOrMany(spec["network"]) {
		section, _ := s.(map[string]any)
		for _, v := range oneOrMany(section["vrf"]) {
			if v == nil {
				continue
			}
			value, ok := v.(string)
			if !ok || !plainInterface.MatchString(value) || vrf != "" && value != vrf {
				return "", fmt.Errorf("Network %s: VRF= names one VRF by its name, not %q", name, fmt.Sprint(v))
			}
			vrf = value
		}
	}
	return vrf, nil
}

// renderRouterNetworkd writes the networkd documents of in as renderNetworkd does, with the units
// of the VRF documents: the device's 00-vrf-<name>.netdev, of Kind=vrf and the VRF's table, and
// 00-vrf-<name>.network, which brings the device up, and VRF=<name> in the [Network] section of
// each Network document whose links the VRF holds.
func renderRouterNetworkd(schemas string, in map[string]any, out string) error {
	d, err := readRouterDocuments(in)
	if err != nil {
		return err
	}
	byKind, _ := in["documents"].(map[string]any)
	docs, err := withVRFUnits(d.VRFs, d.Members, byKind)
	if err != nil {
		return err
	}
	return renderNetworkd(schemas, map[string]any{"node": in["node"], "documents": docs}, out)
}

// withVRFUnits is byKind with each VRF's NetDev and Network documents added and VRF= set in the
// Network documents of its links. It refuses a networkd document of a VRF unit's name and a
// NetDev that creates a device of a VRF's name.
func withVRFUnits(vrfs []named[vrfSpec], member map[string]string, byKind map[string]any) (map[string]any, error) {
	isVRF := func(name string) bool {
		return slices.ContainsFunc(vrfs, func(v named[vrfSpec]) bool { return v.Name == name })
	}
	for _, kind := range []string{"NetDev", "Network"} {
		docs, _ := byKind[kind].([]any)
		for _, d := range docs {
			doc, _ := d.(map[string]any)
			name, err := documentName(kind, doc)
			if err != nil {
				return nil, err
			}
			if v, ok := strings.CutPrefix(name, vrfUnitPrefix); ok && isVRF(v) {
				return nil, fmt.Errorf("%s %s takes the name of VRF %s's units; rename the document", kind, name, v)
			}
			if kind != "NetDev" {
				continue
			}
			spec, _ := doc["spec"].(map[string]any)
			for _, s := range oneOrMany(spec["netDev"]) {
				section, _ := s.(map[string]any)
				if device, _ := section["name"].(string); isVRF(device) {
					return nil, fmt.Errorf("NetDev %s creates the device %s, which VRF %s creates", name, device, device)
				}
			}
		}
	}
	netdevs, _ := byKind["NetDev"].([]any)
	networks, _ := byKind["Network"].([]any)
	netdevs, networks = slices.Clone(netdevs), slices.Clone(networks)
	for i, d := range networks {
		doc, _ := d.(map[string]any)
		spec, _ := doc["spec"].(map[string]any)
		vrf := ""
		// vrfLinks found one VRF for all links of a Network, or none.
		for _, l := range matchLinks(spec) {
			vrf = cmp.Or(vrf, member[l])
		}
		if isVRF(vrf) {
			networks[i] = withNetworkVRF(doc, vrf)
		}
	}
	for _, v := range vrfs {
		unit := map[string]any{"name": vrfUnitPrefix + v.Name}
		netdevs = append(netdevs, map[string]any{"metadata": unit, "spec": map[string]any{
			"netDev": map[string]any{"kind": "vrf", "name": v.Name},
			"vrf":    map[string]any{"table": json.Number(strconv.FormatUint(uint64(v.Spec.Table), 10))},
		}})
		networks = append(networks, map[string]any{"metadata": unit, "spec": map[string]any{
			"match": map[string]any{"name": v.Name},
		}})
	}
	out := maps.Clone(byKind)
	if out == nil {
		out = map[string]any{}
	}
	out["NetDev"], out["Network"] = netdevs, networks
	return out, nil
}

// withNetworkVRF is a copy of the Network document doc with VRF=vrf in its first [Network]
// section, which it gains when it has none.
func withNetworkVRF(doc map[string]any, vrf string) map[string]any {
	spec, _ := doc["spec"].(map[string]any)
	spec = maps.Clone(spec)
	if spec == nil {
		spec = map[string]any{}
	}
	withVRF := func(s any) map[string]any {
		section, _ := s.(map[string]any)
		section = maps.Clone(section)
		if section == nil {
			section = map[string]any{}
		}
		section["vrf"] = vrf
		return section
	}
	switch sections := spec["network"].(type) {
	case []any:
		if len(sections) == 0 {
			spec["network"] = withVRF(nil)
			break
		}
		sections = slices.Clone(sections)
		sections[0] = withVRF(sections[0])
		spec["network"] = sections
	default:
		spec["network"] = withVRF(sections)
	}
	out := maps.Clone(doc)
	out["spec"] = spec
	return out
}
