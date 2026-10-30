package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
)

// The VRF documents: each link's VRF, which the outputs frr and router-nftables read, and the
// units of the VRFs, which the output networkd renders on router images beside the networkd
// documents. The VRF and BGPPeer documents' interfaces are names or a prefix and *, as the router
// kinds' schemas take them, while the words of a Network's Name= are fnmatch globs, as networkd
// reads them; the renderer keeps the two models on purpose and compares them with path.Match.

// defaultVRF names the VRF of every link no VRF holds, as FRR and the router kinds name it.
const defaultVRF = "default"

// vrfUnitPrefix starts the names of a VRF device's units, so that their .network sorts before
// most of the node's own. A Network whose name sorts before it may still match the device first,
// and vrfLinks refuses one that does.
const vrfUnitPrefix = "00-vrf-"

// vrfMembership is the VRF of each Network document, as vrfLinks finds them, the units that put
// links in them, and the globs of the links each VRF holds.
type vrfMembership struct {
	// Networks are the Network documents, by name, with the VRF of the links they are the first
	// to match; a Network of no VRF's links is not in the map.
	Networks map[string]string
	// Units are the .network units networkd reads, sorted as it picks them.
	Units []networkUnit
	// Members are the interfaces of the VRF documents and the words of Name= of the Networks
	// whose own VRF= names a VRF. vrfLinks checked that each Network it puts in a VRF matches
	// links these cover, so they cover the links networkd puts in each VRF.
	Members []vrfMember
}

// linkVRF is the VRF networkd puts the link in: that of the .network unit it picks for the link,
// the default VRF for a link of no VRF's unit or of none. The units that may be the first to
// match the link are in its VRF once vrfLinks has checked them. When only units whose other
// [Match] keys may not match the link do, networkd may also leave the link in no VRF, and
// linkVRF refuses it unless its VRF is the default one.
func (m vrfMembership) linkVRF(link string) (string, error) {
	cs := candidates(m.Units, link)
	if len(cs) == 0 {
		return defaultVRF, nil
	}
	unitVRF := func(u networkUnit) string {
		if vrf, ok := m.Networks[u.name]; ok {
			return vrf
		}
		return defaultVRF
	}
	vrf := unitVRF(cs[0])
	for _, u := range cs[1:] {
		if other := unitVRF(u); other != vrf {
			return "", fmt.Errorf("Network %s and Network %s may each be the first to match %s, which puts it in VRF %s or in VRF %s", cs[0].name, u.name, link, vrf, other)
		}
	}
	if last := cs[len(cs)-1]; last.narrowed && vrf != defaultVRF {
		return "", fmt.Errorf("networkd puts %s in VRF %s only if the other [Match] keys of Network %s match it; give the link a Network that matches it by Name= alone", link, vrf, last.name)
	}
	return vrf, nil
}

// links are the links the VRF holds as nftables matches them: its members, once each, without
// those another of them covers. It refuses a member of a Network's Name= that is neither a name
// nor a prefix and *, which nftables cannot match.
func (m vrfMembership) links(vrf string) ([]string, error) {
	var globs []string
	for _, mem := range m.Members {
		if mem.vrf != vrf {
			continue
		}
		if mem.text == "" || !interfaceName.MatchString(mem.text) {
			return nil, fmt.Errorf("VRF %s holds the links of Network %s's Name=%s, which nftables cannot match; name them by a name or a prefix and *", vrf, mem.network, mem.text)
		}
		globs = append(globs, mem.text)
	}
	slices.Sort(globs)
	globs = slices.Compact(globs)
	return slices.DeleteFunc(slices.Clone(globs), func(g string) bool {
		return slices.ContainsFunc(globs, func(o string) bool { return o != g && globContains(o, g) })
	}), nil
}

// interfaceVRFs are the VRFs whose links the interface or glob i, as a zone names it, may be,
// sorted, defaultVRF standing for links of no VRF. A literal interface is in the VRF networkd
// puts it in. A glob may be links of each VRF a member of which it overlaps, and links of no VRF
// unless a member covers it.
func (m vrfMembership) interfaceVRFs(i string) ([]string, error) {
	if isLiteral(i) {
		vrf, err := m.linkVRF(i)
		if err != nil {
			return nil, err
		}
		return []string{vrf}, nil
	}
	in, covered := map[string]bool{}, false
	for _, mem := range m.Members {
		if globsOverlap(mem.glob, i) {
			in[mem.vrf] = true
		}
		covered = covered || globContains(mem.glob, i)
	}
	if !covered {
		in[defaultVRF] = true
	}
	return slices.Sorted(maps.Keys(in)), nil
}

// vrfMember is a glob of links a VRF holds: an interface of its document, or a word of the Name=
// of the Network document network, whose own VRF= names the VRF. text is the glob as the
// document gives it.
type vrfMember struct {
	vrf, glob, text, network string
}

// networkUnit is a .network unit as networkd picks it for a link: the first one by file name
// whose [Match] matches. vrfLinks reads Name=, and whether other keys of [Match] narrow it, which
// it cannot match: the documents give no link's address, driver or other properties.
type networkUnit struct {
	name  string
	match nameMatch
	// vrf is the unit's own VRF=, and device tells a VRF device's unit.
	vrf    string
	device bool
	// narrowed tells a unit whose [Match] holds other keys, which may match a link its Name=
	// matches, or not.
	narrowed bool
}

// vrfLinks checks the VRF documents against the Network documents of byKind, which name the
// links, as their rules do and where the rules cannot, and returns the VRF of each Network. A VRF
// holds the links its document's interfaces match and those a Network document's Name= matches
// whose own VRF= names it, whether a document of the VRF exists or not. A Network document is in
// the VRF of the links it may be the first to match, and vrfLinks refuses unless all links it may
// match are in that VRF, so that VRF= on the Network puts in the VRF the links that the VRF
// holds, and only those. It refuses a link two VRFs may hold, a literal interface of a VRF no
// Network matches by Name= alone, and a VRF device whose first Network is not its own. Without
// VRF documents and VRF= it checks nothing and puts every link in the default VRF, so that a
// router image without VRFs takes the Network documents it took before them.
func vrfLinks(vrfs []named[vrfSpec], links []string, byKind map[string]any) (vrfMembership, error) {
	var none vrfMembership
	if len(vrfs) == 0 && !anyNetworkVRF(byKind) {
		return none, nil
	}
	var members []vrfMember
	tables := map[uint32]string{}
	for _, v := range vrfs {
		what := "VRF " + v.Name
		if v.Name == defaultVRF || len(v.Name) > 15 {
			return none, fmt.Errorf("%s: a VRF is named as its device, in 15 characters at most, and not %s", what, defaultVRF)
		}
		if slices.Contains(links, v.Name) {
			return none, fmt.Errorf("%s: a Network document names the link %s, the VRF's device", what, v.Name)
		}
		if t := v.Spec.Table; t == 0 || t >= 253 && t <= 255 {
			return none, fmt.Errorf("%s: table %d is no table of a VRF; 0, 253, 254 and 255 are the kernel's", what, t)
		}
		if other, ok := tables[v.Spec.Table]; ok {
			return none, fmt.Errorf("VRF %s and VRF %s both use table %d", other, v.Name, v.Spec.Table)
		}
		tables[v.Spec.Table] = v.Name
		for _, i := range v.Spec.Interfaces {
			if !interfaceName.MatchString(i) {
				return none, fmt.Errorf("%s: %q is no interface name or glob", what, i)
			}
			members = append(members, vrfMember{vrf: v.Name, glob: i, text: i})
		}
	}
	for _, m := range members {
		for _, v := range vrfs {
			if ok, _ := path.Match(m.glob, v.Name); ok {
				return none, fmt.Errorf("VRF %s's %s covers the device of VRF %s", m.vrf, m.glob, v.Name)
			}
		}
	}
	units, err := networkUnits(vrfs, byKind)
	if err != nil {
		return none, err
	}
	for _, u := range units {
		if u.vrf != "" {
			for _, w := range u.match.positives() {
				members = append(members, vrfMember{vrf: u.vrf, glob: w.glob, text: w.text, network: u.name})
			}
		}
	}
	if err := checkVRFMembers(members); err != nil {
		return none, err
	}
	for _, v := range vrfs {
		if u, own := candidates(units, v.Name)[0], vrfUnitPrefix+v.Name; u.name != own {
			if u.narrowed {
				return none, fmt.Errorf("Network %s may match the device of VRF %s before %s, should its other [Match] keys match it; rename it to sort after %s", u.name, v.Name, own, own)
			}
			return none, fmt.Errorf("Network %s matches the device of VRF %s before %s; rename it to sort after %s", u.name, v.Name, own, own)
		}
	}
	for _, m := range members {
		if cs := candidates(units, m.glob); m.network == "" && isLiteral(m.glob) && (len(cs) == 0 || cs[len(cs)-1].narrowed) {
			return none, fmt.Errorf("VRF %s names the interface %s, which no Network document's [Match] Name= matches without other [Match] keys; VRF= reaches a link through its Network document, so give it one", m.vrf, m.glob)
		}
	}
	networks, err := firstNetworks(units, members)
	if err != nil {
		return none, err
	}
	if err := checkNetworkVRFs(units, members, networks); err != nil {
		return none, err
	}
	return vrfMembership{Networks: networks, Units: units, Members: members}, nil
}

// anyNetworkVRF reports whether a Network document of byKind gives VRF=, or a value of VRF= that
// networkVRF refuses.
func anyNetworkVRF(byKind map[string]any) bool {
	docs, _ := byKind["Network"].([]any)
	for _, d := range docs {
		doc, _ := d.(map[string]any)
		spec, _ := doc["spec"].(map[string]any)
		if vrf, err := networkVRF("", spec); vrf != "" || err != nil {
			return true
		}
	}
	return false
}

// networkUnits are the .network units networkd reads: the Network documents of byKind and the
// units of the VRFs' devices, sorted by file name as networkd sorts them.
func networkUnits(vrfs []named[vrfSpec], byKind map[string]any) ([]networkUnit, error) {
	var units []networkUnit
	docs, _ := byKind["Network"].([]any)
	for _, d := range docs {
		doc, _ := d.(map[string]any)
		name, err := documentName("Network", doc)
		if err != nil {
			return nil, err
		}
		spec, _ := doc["spec"].(map[string]any)
		vrf, err := networkVRF(name, spec)
		if err != nil {
			return nil, err
		}
		match, err := parseNameMatch(name, spec)
		if err != nil {
			return nil, err
		}
		narrowed := otherMatchKeys(spec)
		if len(match.words) == 0 && !narrowed {
			// networkd ignores a .network whose [Match] is empty.
			continue
		}
		units = append(units, networkUnit{name: name, match: match, vrf: vrf, narrowed: narrowed})
	}
	for _, v := range vrfs {
		match := nameMatch{words: []nameWord{{glob: v.Name, text: v.Name}}}
		units = append(units, networkUnit{name: vrfUnitPrefix + v.Name, match: match, device: true})
	}
	slices.SortStableFunc(units, func(a, b networkUnit) int { return strings.Compare(a.name+".network", b.name+".network") })
	return units, nil
}

// otherMatchKeys reports whether a Network document's spec gives [Match] keys besides Name=. An
// empty value resets a key, as systemd reads it, so a key whose last value is empty is not given.
func otherMatchKeys(spec map[string]any) bool {
	given := map[string]bool{}
	for _, s := range oneOrMany(spec["match"]) {
		section, _ := s.(map[string]any)
		for k, v := range section {
			if k == "name" {
				continue
			}
			for _, value := range oneOrMany(v) {
				if value == nil {
					continue
				}
				text, isText := value.(string)
				given[k] = !isText || strings.Trim(text, unitWhitespace) != ""
			}
		}
	}
	for _, g := range given {
		if g {
			return true
		}
	}
	return false
}

// candidates are the sorted units that may be the first to match the link: those whose Name=
// matches it, up to the first that other [Match] keys do not narrow.
func candidates(units []networkUnit, link string) []networkUnit {
	var cs []networkUnit
	for _, u := range units {
		if u.match.matches(link) {
			cs = append(cs, u)
			if !u.narrowed {
				break
			}
		}
	}
	return cs
}

// checkVRFMembers refuses globs of two VRFs that may match one link.
func checkVRFMembers(members []vrfMember) error {
	for i, a := range members {
		for _, b := range members[i+1:] {
			if a.vrf == b.vrf || !globsOverlap(a.glob, b.glob) {
				continue
			}
			switch {
			case a.network == "" && b.network == "" && (isLiteral(a.glob) || isLiteral(b.glob)):
				link := a.glob
				if !isLiteral(link) {
					link = b.glob
				}
				return fmt.Errorf("the link %s is in VRF %s and in VRF %s", link, a.vrf, b.vrf)
			case a.network == "" && b.network == "":
				return fmt.Errorf("VRF %s's %s and VRF %s's %s may hold one link", a.vrf, a.glob, b.vrf, b.glob)
			case a.network == "" || b.network == "":
				if a.network == "" {
					a, b = b, a
				}
				return fmt.Errorf("Network %s gives its links VRF=%s, but VRF %s holds them", a.network, a.vrf, b.vrf)
			default:
				return fmt.Errorf("Network %s gives VRF=%s and Network %s VRF=%s to links both may match", a.network, a.vrf, b.network, b.vrf)
			}
		}
	}
	return nil
}

// firstNetworks are the Network documents that may be the first unit to match a member's links,
// by name, with the member's VRF. Of a literal member those are its candidates. Of a glob it is
// each unit that may match some of its links, up to the first unit that matches all of them,
// unless the units before it match all of the links its words share with the glob. A unit that
// other [Match] keys narrow matches no link for certain, so it neither ends the units nor hides
// the links from later ones.
func firstNetworks(units []networkUnit, members []vrfMember) (map[string]string, error) {
	networks := map[string]string{}
	take := func(u networkUnit, m vrfMember, after bool) error {
		if u.device {
			return nil
		}
		if m.network != "" && u.name != m.network && u.vrf != m.vrf {
			return shadowsVRF(units, u, m, after)
		}
		if other, ok := networks[u.name]; ok && other != m.vrf {
			in := []string{other, m.vrf}
			slices.Sort(in)
			return fmt.Errorf("Network %s matches links of %s; the links of one Network are in one VRF", u.name, vrfList(in))
		}
		networks[u.name] = m.vrf
		return nil
	}
	for _, m := range members {
		after := false
		if isLiteral(m.glob) {
			for _, u := range candidates(units, m.glob) {
				if err := take(u, m, after); err != nil {
					return nil, err
				}
				after = after || u.name == m.network
			}
			continue
		}
		var before []nameMatch
		for _, u := range units {
			if u.match.mayMatch(m.glob) && !shadowed(u.match, m.glob, before) {
				if err := take(u, m, after); err != nil {
					return nil, err
				}
			}
			after = after || u.name == m.network
			if u.narrowed {
				continue
			}
			if u.match.covers(m.glob) {
				break
			}
			before = append(before, u.match)
		}
	}
	return networks, nil
}

// shadowsVRF is the refusal of the unit u, of no VRF= of m's VRF, that may match links of the
// member m of a Network's own VRF= first: before that Network, or after it when the Network's
// other [Match] keys or negated words may leave the links to u. VRF= on u would put the links in
// the VRF where networkd does not, so the renderer refuses rather than give u one.
func shadowsVRF(units []networkUnit, u networkUnit, m vrfMember, after bool) error {
	var owner networkUnit
	if i := slices.IndexFunc(units, func(o networkUnit) bool { return o.name == m.network }); i >= 0 {
		owner = units[i]
	}
	// A member of no text stands for the links of a Name= of negated words alone, or of none.
	links, them := "links of "+m.text, "them"
	switch {
	case isLiteral(m.glob):
		links, them = m.glob, "it"
	case m.text != "":
	case len(owner.match.words) == 0:
		links = "links Network " + m.network + " may match"
	case after && !owner.narrowed:
		links = "links Network " + m.network + "'s Name= negates"
	default:
		links = "links Network " + m.network + "'s Name= does not negate"
	}
	if after {
		when := "Network " + m.network + " does not"
		if owner.narrowed {
			when = "the other [Match] keys of Network " + m.network + " do not"
		}
		return fmt.Errorf("Network %s matches %s when %s, and networkd then does not apply %s's VRF=%s to %s; give %s VRF=%s", u.name, links, when, m.network, m.vrf, them, u.name, m.vrf)
	}
	first, applies := "is the first", "never applies"
	if u.narrowed {
		first, applies = "may be the first", "may not apply"
	}
	// Sorted after a Network whose other [Match] keys may not match, u may still match the links.
	fix := fmt.Sprintf("give %s VRF=%s, rename it to sort after %s, or drop %s's VRF=", u.name, m.vrf, m.network, m.network)
	if owner.narrowed {
		fix = fmt.Sprintf("give %s VRF=%s or drop %s's VRF=", u.name, m.vrf, m.network)
	}
	return fmt.Errorf("Network %s %s to match %s, so networkd %s Network %s's VRF=%s to %s; %s", u.name, first, links, applies, m.network, m.vrf, them, fix)
}

// shadowed reports whether units before one of Name= m match every link that m's words share
// with the glob, so that m matches none of them first.
func shadowed(m nameMatch, glob string, before []nameMatch) bool {
	for _, w := range m.positives() {
		if globsOverlap(w.glob, glob) && !slices.ContainsFunc(before, func(b nameMatch) bool { return b.covers(w.glob) }) {
			return false
		}
	}
	return true
}

// checkNetworkVRFs refuses a Network of a VRF that may match links outside the VRF, or whose own
// VRF= names another VRF.
func checkNetworkVRFs(units []networkUnit, members []vrfMember, networks map[string]string) error {
	for _, u := range units {
		vrf, ok := networks[u.name]
		if !ok {
			continue
		}
		if u.vrf != "" && u.vrf != vrf {
			return fmt.Errorf("Network %s gives its links VRF=%s, but VRF %s holds them", u.name, u.vrf, vrf)
		}
		for _, w := range u.match.positives() {
			if slices.ContainsFunc(members, func(m vrfMember) bool { return m.vrf == vrf && globContains(m.glob, w.glob) }) {
				continue
			}
			switch {
			case len(u.match.words) == 0:
				return fmt.Errorf("Network %s matches by its other [Match] keys alone, as its Name= holds no word networkd takes, so it may match links of VRF %s and links outside it; name its links", u.name, vrf)
			case w.text == "":
				return fmt.Errorf("Network %s matches every link its Name= does not negate, links of VRF %s and links outside it; name its links", u.name, vrf)
			case isLiteral(w.glob):
				in := []string{"", vrf}
				for _, m := range members {
					if ok, _ := path.Match(m.glob, w.glob); ok {
						in[0] = m.vrf
					}
				}
				slices.Sort(in)
				return fmt.Errorf("Network %s matches links of %s; the links of one Network are in one VRF", u.name, vrfList(in))
			default:
				return fmt.Errorf("Network %s's Name=%s may match links of VRF %s and links outside it; name its links", u.name, w.text, vrf)
			}
		}
	}
	return nil
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

// networkVRF is the VRF= a Network document's [Network] sections give, "" for none. An empty
// VRF= drops the one before it, as systemd reads it.
func networkVRF(name string, spec map[string]any) (string, error) {
	vrf := ""
	for _, s := range oneOrMany(spec["network"]) {
		section, _ := s.(map[string]any)
		for _, v := range oneOrMany(section["vrf"]) {
			if v == nil {
				continue
			}
			if v == "" {
				vrf = ""
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
// 00-vrf-<name>.network, which brings the device up, leaves it out of the node's online state
// and ends the table in unreachable default routes, and VRF=<name> in the [Network] section of
// each Network document that is the first to match links the VRF holds.
func renderRouterNetworkd(schemas string, in map[string]any, out string) error {
	d, err := readRouterDocuments(in)
	if err != nil {
		return err
	}
	byKind, _ := in["documents"].(map[string]any)
	docs, err := withVRFUnits(d.VRFs, d.Membership.Networks, byKind)
	if err != nil {
		return err
	}
	return renderNetworkd(schemas, map[string]any{"node": in["node"], "documents": docs}, out)
}

// withVRFUnits is byKind with each VRF's NetDev and Network documents added and VRF= set in each
// Network document that networks puts in a VRF without its own VRF= of it, which only a VRF
// document's interfaces do. It refuses a networkd document of a VRF unit's name and a NetDev
// that creates a device of a VRF's name.
func withVRFUnits(vrfs []named[vrfSpec], networks map[string]string, byKind map[string]any) (map[string]any, error) {
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
				for _, n := range oneOrMany(section["name"]) {
					if device, _ := n.(string); isVRF(device) {
						return nil, fmt.Errorf("NetDev %s creates the device %s, which VRF %s creates", name, device, device)
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
		name, _ := documentName("Network", doc)
		spec, _ := doc["spec"].(map[string]any)
		own, _ := networkVRF(name, spec)
		if vrf, ok := networks[name]; ok && own != vrf {
			docs[i] = withNetworkVRF(doc, vrf)
		}
	}
	for _, v := range vrfs {
		unit := map[string]any{"name": vrfUnitPrefix + v.Name}
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
		section["vrf"] = append(slices.Clone(oneOrMany(old)), vrf)
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
// interfaces that matches one: bgpd runs no session on the device, and the input hook sees it
// as the iifname of every packet from the VRF's links, so router-nftables would open the peer's
// ports on all of them.
func checkPeerDevices(s peerSpec, vrfs []named[vrfSpec]) error {
	for _, v := range vrfs {
		if s.Interface != nil && *s.Interface == v.Name {
			return fmt.Errorf("interface %s is the device of VRF %s", v.Name, v.Name)
		}
		for _, glob := range s.Interfaces {
			if matchesGlob(glob, v.Name) {
				return fmt.Errorf("interfaces: %s matches %s, the device of VRF %s", glob, v.Name, v.Name)
			}
		}
	}
	return nil
}
