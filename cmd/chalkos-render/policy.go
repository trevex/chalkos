package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/router/derive"
)

// Policy routing: the PolicyRoute documents' rules mark the packets they match in the chain
// policy-routing of the firewall, a mark per target table, and a rule of the kernel per mark sends
// the marked packets to the table. networkd keeps the kernel's rules and the NextHops' tables:
// router-networkd renders the rules and each NextHop table's unreachable default routes into the
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
}

// policyTargets are the distinct tables of the PolicyRoute documents' targets, sorted by number,
// each with its mark, (index+1)<<16. Rules that name one table share its mark: rules of one VRF,
// of one table or of one NextHop. The schemas and rules keep the targets' tables apart: a VRF's
// and a NextHop's table are their own, and a rule's table is neither. NextHop documents that no
// rule names take no mark. It refuses more than 255 tables, which the mark's bits cannot tell
// apart, a count that spans every document: the refusal names the rule whose target is the 256th
// table, in the order of the documents and their rules, and counts them all.
func policyTargets(d derive.Model, t derive.PolicyTables) ([]routeTarget, error) {
	tables := map[uint32]bool{}
	// past and pastRule name the rule whose target is the 256th table, if any.
	past, pastRule := "", -1
	for _, p := range d.PolicyRoutes {
		for i, r := range p.Spec.Rules {
			table, err := t.Table(r)
			if err != nil {
				return nil, derive.Within(fmt.Sprintf("PolicyRoute %s rule %d", p.Name, i+1), err)
			}
			if !tables[table] && len(tables) == 255 && pastRule < 0 {
				past, pastRule = p.Name, i
			}
			tables[table] = true
		}
	}
	if pastRule >= 0 {
		return nil, refusals.Errorf("PolicyRoute", past, fmt.Sprintf("spec.rules[%d].to", pastRule), "the PolicyRoute documents send traffic to %d tables, more than the 255 their marks tell apart", len(tables))
	}
	targets := make([]routeTarget, 0, len(tables))
	for i, table := range slices.Sorted(maps.Keys(tables)) {
		targets = append(targets, routeTarget{table: table, mark: uint32(i+1) << policyMarkShift})
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

// policyLines are the lines of the chain policy-routing: a packet another pass marked keeps its
// mark, a packet to an address of the router in the VRF of its link takes no mark, then each rule
// of each PolicyRoute document in order marks the packets it matches with the mark of its target
// and ends the chain, counting them. A document's in matches the links themselves, as prerouting
// sees a packet from a VRF's link first with the link, the pass whose mark the routing decision
// after the VRF's pass keeps.
//
// The kernel puts the local routes of a VRF's links and device into the VRF's table, where the
// rule of the local table at priority 0 does not see them, and a mark's rule at 900 would send
// the packet past the VRF's rule at 1000. fib without iif looks the address up in the table of
// the VRF of the packet's link, on the link's pass and on the VRF device's alike; with iif it
// would find local only an address of that very link.
func (f *firewall) policyLines(d derive.Model) ([]string, error) {
	lines := []string{fmt.Sprintf("meta mark & 0x%08x != 0 return", policyMarkMask), "fib daddr type local return"}
	for _, p := range d.PolicyRoutes {
		head, err := f.policyHead(p)
		if err != nil {
			return nil, err
		}
		for i, r := range p.Spec.Rules {
			what := fmt.Sprintf("PolicyRoute %s rule %d", p.Name, i+1)
			matches, err := f.policyMatches(r)
			if err != nil {
				return nil, derive.Within(what, err)
			}
			table, err := f.tables.Table(r)
			if err != nil {
				return nil, derive.Within(what, err)
			}
			mark := markOf(f.targets, table)
			set := fmt.Sprintf("counter meta mark set meta mark & 0x%08x | 0x%08x return comment %q", ^uint32(policyMarkMask), mark, fmt.Sprintf("%s %d", p.Name, i+1))
			for _, m := range matches {
				lines = append(lines, joinWords(head, m, set))
			}
		}
	}
	return lines, nil
}

// policyHead matches where a PolicyRoute's traffic comes in, as prerouting sees it: the links of
// its zone, its interfaces or, without in, every link. The kind's rules check that in names a zone
// or interfaces, one of them. It refuses a zone that holds no link, an interface or glob that
// matches a VRF's device, which prerouting sees for a VRF's link only after the pass that decides
// the mark, and a zone's link or an interface that matches a NAT64's link, whose traffic Jool
// translates. The devices include those of the Networks' own VRF=, which the rules cannot see.
func (f *firewall) policyHead(p derive.Named[derive.PolicyRouteSpec]) (string, error) {
	what := "PolicyRoute " + p.Name
	in := p.Spec.In
	switch {
	case in == nil:
		return "", nil
	case in.Zone != nil:
		z, err := derive.MustGet(f.zones, "FirewallZone", *in.Zone)
		if err != nil {
			return "", derive.Within(what, err)
		}
		if len(z.links) == 0 {
			return "", refusals.Errorf("PolicyRoute", p.Name, "spec.in.zone", "the zone %s holds no link", *in.Zone)
		}
		for _, l := range z.links {
			if link, n, ok := f.nat64Link(l); ok {
				return "", refusals.Errorf("PolicyRoute", p.Name, "spec.in.zone", "the zone %s holds %s, which matches %s, the link of NAT64 %s, whose traffic Jool translates; name the zone's other links in interfaces", *in.Zone, l, link, n)
			}
		}
		return "iifname " + interfaceSet(z.links), nil
	default:
		if err := checkInterfaces(what, in.Interfaces); err != nil {
			return "", err
		}
		for j, i := range in.Interfaces {
			at := fmt.Sprintf("spec.in.interfaces[%d]", j)
			if dev, ok := vrfDeviceMatch(i, f.vrfDevices); ok {
				return "", refusals.Errorf("PolicyRoute", p.Name, at, "%s matches %s, the device of VRF %s; name the VRF's links or a zone of the VRF", i, dev, dev)
			}
			if link, n, ok := f.nat64Link(i); ok {
				return "", refusals.Errorf("PolicyRoute", p.Name, at, "%s matches %s, the link of NAT64 %s, whose traffic Jool translates; name other links", i, link, n)
			}
		}
		return "iifname " + interfaceSet(in.Interfaces), nil
	}
}

// nat64Link is the link of the first NAT64 whose link the interface or glob i matches, and the
// NAT64's name.
func (f *firewall) nat64Link(i string) (string, string, bool) {
	for _, n := range f.nat64 {
		if link := derive.NAT64LinkPrefix + n.Name; matchesGlob(i, link) {
			return link, n.Name, true
		}
	}
	return "", "", false
}

// policyMatches are a rule's matches, one per family as a firewall rule's, with its DSCP values
// in front, which nftables matches in each family by its own word. A rule with a next hop matches
// the NextHop's family alone. The kind's rules check that the match has a term, that it leaves a
// family, and that the NextHop's family is one the match leaves.
func (f *firewall) policyMatches(r derive.PolicyRule) ([]string, error) {
	m := r.Match
	fr := derive.FirewallRule{Family: m.Family, Protocol: m.Protocol, SourcePorts: m.SourcePorts, DestinationPorts: m.DestinationPorts, Source: m.Source, Destination: m.Destination}
	if name := r.To.NextHop; name != nil {
		h, err := derive.MustGet(f.tables.NextHops, "NextHop", *name)
		if err != nil {
			return nil, err
		}
		fam := "ipv6"
		if h.Address.Is4() {
			fam = "ipv4"
		}
		fr.Family = &fam
	}
	dscp := make([]string, len(m.DSCP))
	for i, v := range m.DSCP {
		dscp[i] = strconv.Itoa(v)
	}
	matches, err := f.matchLines(fr)
	if err != nil {
		return nil, err
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

// withPolicyRoutes is docs, the networkd documents with the VRFs' and NAT64s' units, with the
// kernel's rules of the PolicyRoute documents' targets and the unreachable default routes of every
// NextHop's table in the Network of lo, and the default route through each NextHop in the Network
// of its link. A NextHop that no rule names gets its table too, so that the units do not depend on
// which NextHops the rules name. A Network document that networkd picks for lo or for the link
// takes them when it matches that link alone by its Name=; without one for lo, a Network
// 00-policy-routing of lo holds them. It refuses a NextHop's link no Network matches, a Network
// that may match a NextHop's link by other [Match] keys, or lo as loNetwork finds, or that matches
// other links too, which would get the routes, and a Network document named 00-policy-routing.
// Without PolicyRoute and NextHop documents it changes nothing.
func withPolicyRoutes(d derive.Model, docs map[string]any) (map[string]any, error) {
	tables, err := d.PolicyTables()
	if err != nil {
		return nil, err
	}
	targets, err := policyTargets(d, tables)
	if err != nil || len(targets) == 0 && len(tables.Hops) == 0 {
		return docs, err
	}
	units, err := d.Units()
	if err != nil {
		return nil, err
	}
	if slices.ContainsFunc(units, func(u derive.Unit) bool { return u.Name == policyUnit }) {
		return nil, refusals.Errorf("Network", policyUnit, "metadata.name", "Network %s takes the name of the policy routing's unit; rename the document", policyUnit)
	}
	networks, _ := docs["Network"].([]any)
	networks = slices.Clone(networks)
	// add appends the sections, if any, to the key of the Network document of that name.
	add := func(name, key string, sections []any) {
		if len(sections) == 0 {
			return
		}
		for i, n := range networks {
			doc, _ := n.(map[string]any)
			if got, _ := derive.DocumentName("Network", doc); got == name {
				networks[i] = withSections(doc, key, sections)
				return
			}
		}
	}
	var rules, unreachable []any
	for _, t := range targets {
		rules = append(rules, map[string]any{
			"firewallMark": fmt.Sprintf("0x%x/0x%x", t.mark, policyMarkMask),
			"table":        json.Number(strconv.FormatUint(uint64(t.table), 10)),
			"priority":     json.Number(strconv.Itoa(policyRulePriority)),
			"family":       "both",
		})
	}
	for _, h := range tables.Hops {
		unreachable = append(unreachable, unreachableDefaults(h.Table)...)
	}
	lo, err := loNetwork(units)
	if err != nil {
		return nil, derive.Within(policyDocuments(d), err)
	}
	if lo == "" {
		spec := map[string]any{"match": map[string]any{"name": "lo"}}
		if len(rules) > 0 {
			spec["routingPolicyRule"] = rules
		}
		if len(unreachable) > 0 {
			spec["route"] = unreachable
		}
		networks = append(networks, map[string]any{"metadata": map[string]any{"name": policyUnit}, "spec": spec})
	} else {
		add(lo, "routingPolicyRule", rules)
		add(lo, "route", unreachable)
	}
	for _, h := range tables.Hops {
		link, err := soleNetwork(units, h.Link, "NextHop", h.Name, "spec.interface")
		if err != nil {
			return nil, derive.Within("NextHop "+h.Name, err)
		}
		if link == "" {
			return nil, refusals.Errorf("NextHop", h.Name, "spec.interface", "NextHop %s is on %s, which no Network document names; give the link one, which gets the next hop's route", h.Name, h.Link)
		}
		add(link, "route", []any{map[string]any{
			"gateway": h.Address.String(),
			"table":   json.Number(strconv.FormatUint(uint64(h.Table), 10)),
		}})
	}
	out := maps.Clone(docs)
	out["Network"] = networks
	return out, nil
}

// policyDocuments names the documents whose sections go into the Network of lo: the PolicyRoute
// documents with rules, whose targets' rules go there, and the NextHop documents, whose tables'
// unreachable defaults do, as "PolicyRoute a, b and NextHop c".
func policyDocuments(d derive.Model) string {
	var routes, hops []string
	for _, p := range d.PolicyRoutes {
		if len(p.Spec.Rules) > 0 {
			routes = append(routes, p.Name)
		}
	}
	for _, h := range d.NextHops {
		hops = append(hops, h.Name)
	}
	var parts []string
	if len(routes) > 0 {
		parts = append(parts, "PolicyRoute "+strings.Join(routes, ", "))
	}
	if len(hops) > 0 {
		parts = append(parts, "NextHop "+strings.Join(hops, ", "))
	}
	return strings.Join(parts, " and ")
}

// loNetwork is the Network document that networkd picks for lo, "" when the unit of the
// policy routing holds its sections. Without a Network that matches lo by its Name= alone, that
// unit joins the units as networkd sorts them, so that a Network of other [Match] keys refuses
// only when it sorts before the unit, which it may shadow, or when its Name= matches lo, which the
// unit may shadow. A refusal names the Network; a device named lo is a VRF's of that name.
func loNetwork(units []derive.Unit) (string, error) {
	if slices.ContainsFunc(units, func(u derive.Unit) bool { return !u.Narrowed && u.Match.Matches("lo") }) {
		return soleNetwork(units, "lo", "VRF", "lo", "metadata.name")
	}
	own := derive.Unit{Name: policyUnit, Match: derive.NameMatch{Words: []derive.NameWord{{Glob: "lo", Text: "lo"}}}}
	units = append(slices.Clone(units), own)
	slices.SortStableFunc(units, func(a, b derive.Unit) int { return strings.Compare(a.Name+".network", b.Name+".network") })
	if _, err := soleNetwork(units, "lo", "VRF", "lo", "metadata.name"); err != nil {
		return "", err
	}
	for _, u := range units {
		if u.Narrowed && len(u.Match.Words) > 0 && u.Match.Matches("lo") {
			return "", refusals.Errorf("Network", u.Name, "spec.match", "Network %s may match lo by other [Match] keys; name lo in a Network that its Name= alone matches", u.Name)
		}
	}
	return "", nil
}

// soleNetwork is the Network document networkd picks for the link, "" when none matches it. It
// refuses a VRF's device, as a refusal of the field path of the document kind/name that names the
// link, a Network that other [Match] keys may keep from the link, and one whose Name= may match
// other links, which would get what the link gets, both as refusals of the Network.
func soleNetwork(units []derive.Unit, link, kind, name, path string) (string, error) {
	cs := derive.Candidates(units, link)
	if len(cs) == 0 {
		return "", nil
	}
	u := cs[0]
	switch {
	case u.Device:
		return "", refusals.Errorf(kind, name, path, "%s is the device of VRF %s, which gets no next hop", link, link)
	case u.Narrowed:
		return "", refusals.Errorf("Network", u.Name, "spec.match", "Network %s may match %s by other [Match] keys; name %s in a Network that its Name= alone matches", u.Name, link, link)
	}
	for _, w := range u.Match.Positives() {
		if w.Glob != link {
			return "", refusals.Errorf("Network", u.Name, cmp.Or(w.Path, "spec.match"), "Network %s matches %s and other links, which would take its policy routing too; give %s a Network of its own", u.Name, link, link)
		}
	}
	return u.Name, nil
}

// withSections is a copy of the Network document doc with the sections after those of its key.
func withSections(doc map[string]any, key string, sections []any) map[string]any {
	spec, _ := doc["spec"].(map[string]any)
	spec = maps.Clone(spec)
	if spec == nil {
		spec = map[string]any{}
	}
	var have []any
	if v, ok := spec[key]; ok && v != nil {
		have = slices.Clone(derive.OneOrMany(v))
	}
	spec[key] = append(have, sections...)
	out := maps.Clone(doc)
	out["spec"] = spec
	return out
}
