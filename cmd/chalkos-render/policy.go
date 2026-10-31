package main

import (
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// Policy routing: the PolicyRoute documents' rules mark the packets they match in the chain
// policy-routing of the firewall, a mark per target table, and a rule of the kernel per mark sends
// the marked packets to the table. networkd keeps the kernel's rules and the next hops' tables:
// router-networkd renders the rules and each next-hop table's unreachable default routes into the
// Network of lo, which never goes down, and the default route through a next hop into the Network
// of the next hop's link, which networkd adds again each time the link comes up.

const (
	// policyMarkMask are the bits of the packet mark that say a packet's target; Kubernetes,
	// which never shares an image with the router layer, takes single bits outside them.
	policyMarkMask = 0x00ff0000
	// policyMarkShift places a target's number in the mask.
	policyMarkShift = 16
	// policyRulePriority places the kernel's rules of the targets before the l3mdev rule of the
	// VRFs, 1000, so that a policy sends traffic into and out of a VRF.
	policyRulePriority = 900
	// policyUnit names the Network that holds the rules and the unreachable defaults when no
	// Network of the config matches lo.
	policyUnit = "00-policy-routing"
)

// routeTarget is a table the PolicyRoute documents send traffic to, and the mark that sends it.
type routeTarget struct {
	table uint32
	mark  uint32
	// hop is the next hop whose table this is; nil for a VRF's table or a rule's table.
	hop *nextHop
}

// policyTargets are the tables of the PolicyRoute documents' targets, sorted by number, each with
// its mark, (index+1)<<16: a VRF's table, a rule's table and a next hop's table. It checks the
// targets as the kind's rules do: one per rule, a VRF the config declares, a table that is not
// the local table and no VRF's, a next hop of an address, an interface's name and a table of its
// own that is none of the kernel's tables and no VRF's. It refuses more than 255 tables, which
// the mark's bits cannot tell apart.
func policyTargets(d routerDocuments) ([]routeTarget, error) {
	vrfTables := map[string]uint32{}
	tableVRF := map[uint32]string{}
	for _, v := range d.VRFs {
		vrfTables[v.Name] = v.Spec.Table
		tableVRF[v.Spec.Table] = v.Name
	}
	byTable := map[uint32]*routeTarget{}
	for _, p := range d.PolicyRoutes {
		for i, r := range p.Spec.Rules {
			what := fmt.Sprintf("PolicyRoute %s rule %d", p.Name, i+1)
			to := r.To
			n := 0
			for _, set := range []bool{to.VRF != nil, to.Table != nil, to.NextHop != nil} {
				if set {
					n++
				}
			}
			if n != 1 {
				return nil, fmt.Errorf("%s: to names a VRF, a table or a next hop, one of them", what)
			}
			var t routeTarget
			switch {
			case to.VRF != nil:
				table, ok := vrfTables[*to.VRF]
				if !ok {
					return nil, fmt.Errorf("%s: the VRF %s does not exist", what, *to.VRF)
				}
				t.table = table
			case to.Table != nil:
				t.table = *to.Table
				if t.table == 0 || t.table == 255 {
					return nil, fmt.Errorf("%s: table %d is none a policy sends traffic to", what, t.table)
				}
				if v, ok := tableVRF[t.table]; ok {
					return nil, fmt.Errorf("%s: table %d is VRF %s's; name the VRF", what, t.table, v)
				}
			default:
				h := *to.NextHop
				a, err := netip.ParseAddr(h.Address)
				if err != nil || a.Zone() != "" || !a.IsGlobalUnicast() && !a.IsLinkLocalUnicast() {
					return nil, fmt.Errorf("%s: %q is no next hop's address", what, h.Address)
				}
				if !plainInterface.MatchString(h.Interface) || h.Interface == "lo" {
					return nil, fmt.Errorf("%s: %q is no link of a next hop", what, h.Interface)
				}
				if h.Table == 0 || h.Table >= 253 && h.Table <= 255 {
					return nil, fmt.Errorf("%s: table %d of the next hop is one of the kernel's", what, h.Table)
				}
				if v, ok := tableVRF[h.Table]; ok {
					return nil, fmt.Errorf("%s: table %d of the next hop is VRF %s's", what, h.Table, v)
				}
				h.Address = a.String()
				t.table, t.hop = h.Table, &h
			}
			if old, ok := byTable[t.table]; ok {
				if (old.hop == nil) != (t.hop == nil) || old.hop != nil && *old.hop != *t.hop {
					return nil, fmt.Errorf("%s: table %d is another next hop's, or a next hop's and a rule's; give each next hop a table of its own", what, t.table)
				}
				continue
			}
			byTable[t.table] = &t
		}
	}
	if len(byTable) > 255 {
		return nil, fmt.Errorf("the PolicyRoute documents send traffic to %d tables, more than the 255 their marks tell apart", len(byTable))
	}
	targets := make([]routeTarget, 0, len(byTable))
	for i, table := range slices.Sorted(maps.Keys(byTable)) {
		t := *byTable[table]
		t.mark = uint32(i+1) << policyMarkShift
		targets = append(targets, t)
	}
	return targets, nil
}

// markOf is the mark of the table of a rule's target among the targets.
func markOf(targets []routeTarget, table uint32) uint32 {
	for _, t := range targets {
		if t.table == table {
			return t.mark
		}
	}
	return 0
}

// ruleTable is the table of a rule's target, which policyTargets checked.
func ruleTable(d routerDocuments, r policyRule) uint32 {
	switch {
	case r.To.VRF != nil:
		for _, v := range d.VRFs {
			if v.Name == *r.To.VRF {
				return v.Spec.Table
			}
		}
	case r.To.Table != nil:
		return *r.To.Table
	case r.To.NextHop != nil:
		return r.To.NextHop.Table
	}
	return 0
}

// policyLines are the lines of the chain policy-routing: a packet another pass marked keeps its
// mark, then each rule of each PolicyRoute document in order marks the packets it matches with
// the mark of its target and ends the chain, counting them. A document's in matches the links
// themselves, as prerouting sees a packet from a VRF's link first with the link, the pass whose
// mark the routing decision after the VRF's pass keeps.
func (f *firewall) policyLines(d routerDocuments) ([]string, error) {
	lines := []string{fmt.Sprintf("meta mark & 0x%08x != 0 return", policyMarkMask)}
	for _, p := range d.PolicyRoutes {
		head, err := f.policyHead(p)
		if err != nil {
			return nil, err
		}
		for i, r := range p.Spec.Rules {
			matches, err := f.policyMatches(r)
			if err != nil {
				return nil, fmt.Errorf("PolicyRoute %s rule %d: %w", p.Name, i+1, err)
			}
			mark := markOf(f.targets, ruleTable(d, r))
			set := fmt.Sprintf("counter meta mark set meta mark & 0x%08x | 0x%08x return comment %q", ^uint32(policyMarkMask), mark, fmt.Sprintf("%s %d", p.Name, i+1))
			for _, m := range matches {
				lines = append(lines, joinWords(head, m, set))
			}
		}
	}
	return lines, nil
}

// policyHead matches where a PolicyRoute's traffic comes in, as prerouting sees it: the links of
// its zone, its interfaces or, without in, every link. It refuses a zone the config lacks or
// that holds no link, and an interface that matches a VRF's device, which prerouting sees for a
// VRF's link only after the pass that decides the mark, or a NAT64's link.
func (f *firewall) policyHead(p named[policyRouteSpec]) (string, error) {
	what := "PolicyRoute " + p.Name
	in := p.Spec.In
	switch {
	case in == nil:
		return "", nil
	case in.Zone != nil && len(in.Interfaces) == 0:
		z, ok := f.zones[*in.Zone]
		if !ok {
			return "", fmt.Errorf("%s: the zone %s is no FirewallZone", what, *in.Zone)
		}
		if len(z.links) == 0 {
			return "", fmt.Errorf("%s: the zone %s holds no link", what, *in.Zone)
		}
		return "iifname " + interfaceSet(z.links), nil
	case in.Zone == nil && len(in.Interfaces) > 0:
		if err := checkInterfaces(what, in.Interfaces); err != nil {
			return "", err
		}
		for _, i := range in.Interfaces {
			if dev, ok := vrfDeviceMatch(i, f.vrfDevices); ok {
				return "", fmt.Errorf("%s: %s matches %s, the device of VRF %s; name the VRF's links or a zone of the VRF", what, i, dev, dev)
			}
			for _, n := range f.nat64 {
				if link := nat64LinkPrefix + n.name; matchesGlob(i, link) {
					return "", fmt.Errorf("%s: %s matches %s, the link of NAT64 %s, whose traffic Jool translates; name other links", what, i, link, n.name)
				}
			}
		}
		return "iifname " + interfaceSet(in.Interfaces), nil
	}
	return "", fmt.Errorf("%s: in names a zone or interfaces, one of them", what)
}

// policyMatches are a rule's matches, one per family as a firewall rule's, with its DSCP values
// in front, which nftables matches in each family by its own word. A rule with a next hop matches
// the next hop's family alone.
func (f *firewall) policyMatches(r policyRule) ([]string, error) {
	m := r.Match
	if m.Family == nil && m.Protocol == nil && len(m.Source) == 0 && len(m.Destination) == 0 &&
		len(m.SourcePorts) == 0 && len(m.DestinationPorts) == 0 && len(m.DSCP) == 0 {
		return nil, fmt.Errorf("the match is empty; to match every packet, say so, such as with source: [0.0.0.0/0]")
	}
	fr := firewallRule{Family: m.Family, Protocol: m.Protocol, SourcePorts: m.SourcePorts, DestinationPorts: m.DestinationPorts, Source: m.Source, Destination: m.Destination}
	if h := r.To.NextHop; h != nil {
		a, err := netip.ParseAddr(h.Address)
		if err != nil {
			return nil, fmt.Errorf("%q is no next hop's address", h.Address)
		}
		fam := "ipv6"
		if a.Is4() {
			fam = "ipv4"
		}
		if fr.Family != nil && *fr.Family != fam {
			return nil, fmt.Errorf("the next hop %s is of another family than %s", h.Address, *fr.Family)
		}
		fr.Family = &fam
	}
	dscp := make([]string, len(m.DSCP))
	for i, v := range m.DSCP {
		if v < 0 || v > 63 {
			return nil, fmt.Errorf("dscp %d is no value from 0 to 63", v)
		}
		dscp[i] = strconv.Itoa(v)
	}
	matches, err := f.matchLines(fr)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("the match leaves no family, or none of its next hop")
	}
	var out []string
	for _, fm := range matches {
		if len(dscp) == 0 {
			out = append(out, fm.match)
			continue
		}
		families := []string{fm.family}
		if fm.family == "" {
			families = []string{"ipv4", "ipv6"}
		}
		for _, fam := range families {
			rest := strings.TrimPrefix(fm.match, "meta nfproto "+fam)
			out = append(out, joinWords(fmt.Sprintf("%s dscp { %s }", familyWords[fam], strings.Join(dscp, ", ")), strings.TrimSpace(rest)))
		}
	}
	return out, nil
}
