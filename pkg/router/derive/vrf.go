package derive

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/refusals"
)

// The VRF of each link, as networkd puts links in VRFs: by the VRF= of the .network unit it picks
// for the link, which router-networkd gives the Networks of the links a VRF holds. The VRF and
// BGPPeer documents' interfaces are names or a prefix and *, as the router kinds' schemas take
// them, while the words of a Network's Name= are fnmatch globs, as networkd reads them; the model
// keeps the two apart on purpose and compares them with path.Match.

// DefaultVRF names the VRF of every link no VRF holds, as FRR and the router kinds name it.
const DefaultVRF = "default"

// VRFUnitPrefix starts the names of a VRF device's units, so that their .network sorts before
// most of the node's own. A Network whose name sorts before it may still match the device first,
// and vrfLinks refuses one that does.
const VRFUnitPrefix = "00-vrf-"

// Membership is the VRF of each Network document, as vrfLinks finds them, the units that put
// links in them, and the globs of the links each VRF holds.
type Membership struct {
	// Networks are the Network documents, by name, with the VRF of the links they are the first
	// to match; a Network of no VRF's links is not in the map.
	Networks map[string]string
	// Units are the .network units networkd reads, sorted as it picks them; none without VRF
	// documents and VRF=, whose units Model.Units gives.
	Units []Unit
	// Members are the interfaces of the VRF documents and the words of Name= of the Networks
	// whose own VRF= names a VRF. vrfLinks checked that each Network it puts in a VRF matches
	// links these cover, so they cover the links networkd puts in each VRF.
	Members []Member
}

// LinkVRF is the VRF networkd puts the link in: that of the .network unit it picks for the link,
// the default VRF for a link of no VRF's unit or of none. The units that may be the first to
// match the link are in its VRF once vrfLinks has checked them. When only units whose other
// [Match] keys may not match the link do, networkd may also leave the link in no VRF, and
// LinkVRF refuses it unless its VRF is the default one.
func (m Membership) LinkVRF(link string) (string, error) {
	cs := Candidates(m.Units, link)
	if len(cs) == 0 {
		return DefaultVRF, nil
	}
	unitVRF := func(u Unit) string {
		if vrf, ok := m.Networks[u.Name]; ok {
			return vrf
		}
		return DefaultVRF
	}
	vrf := unitVRF(cs[0])
	for _, u := range cs[1:] {
		if other := unitVRF(u); other != vrf {
			return "", refusals.Errorf("Network", u.Name, "spec.match", "Network %s and Network %s may each be the first to match %s, which puts it in VRF %s or in VRF %s", cs[0].Name, u.Name, link, vrf, other)
		}
	}
	if last := cs[len(cs)-1]; last.Narrowed && vrf != DefaultVRF {
		return "", refusals.Errorf("Network", last.Name, "spec.match", "networkd puts %s in VRF %s only if the other [Match] keys of Network %s match it; give the link a Network that matches it by Name= alone", link, vrf, last.Name)
	}
	return vrf, nil
}

// Links are the links the VRF holds as nftables matches them: its members, once each, without
// those another of them covers. It refuses a member of a Network's Name= that is neither a name
// nor a prefix and *, which nftables cannot match.
func (m Membership) Links(vrf string) ([]string, error) {
	var globs []string
	for _, mem := range m.Members {
		if mem.VRF != vrf {
			continue
		}
		if mem.Text == "" || !InterfaceName.MatchString(mem.Text) {
			return nil, refusals.Errorf("Network", mem.Network, mem.Path, "VRF %s holds the links of Network %s's Name=%s, which nftables cannot match; name them by a name or a prefix and *", vrf, mem.Network, mem.Text)
		}
		globs = append(globs, mem.Text)
	}
	slices.Sort(globs)
	globs = slices.Compact(globs)
	return slices.DeleteFunc(slices.Clone(globs), func(g string) bool {
		return slices.ContainsFunc(globs, func(o string) bool { return o != g && globContains(o, g) })
	}), nil
}

// InterfaceVRFs are the VRFs whose links the interface or glob i, as a zone names it, may be,
// sorted, DefaultVRF standing for links of no VRF. A literal interface is in the VRF networkd
// puts it in. A glob may be links of each VRF a member of which it overlaps, and links of no VRF
// unless a member covers it.
func (m Membership) InterfaceVRFs(i string) ([]string, error) {
	if isLiteral(i) {
		vrf, err := m.LinkVRF(i)
		if err != nil {
			return nil, err
		}
		return []string{vrf}, nil
	}
	in, covered := map[string]bool{}, false
	for _, mem := range m.Members {
		if GlobsOverlap(mem.Glob, i) {
			in[mem.VRF] = true
		}
		covered = covered || globContains(mem.Glob, i)
	}
	if !covered {
		in[DefaultVRF] = true
	}
	return slices.Sorted(maps.Keys(in)), nil
}

// Member is a glob of links a VRF holds: an interface of its document, or a word of the Name=
// of the Network document Network, whose own VRF= names the VRF. Text is the glob as the
// document gives it, and Path the field that holds it, which a refusal of the member names: the
// VRF document's spec.interfaces[i], or the Network document's value of Name=, spec.match for a
// Name= of negated words alone. VRFPath is the field of a Network's VRF=.
type Member struct {
	VRF, Glob, Text, Network, Path, VRFPath string
}

// Unit is a .network unit as networkd picks it for a link: the first one by file name
// whose [Match] matches. vrfLinks reads Name=, and whether other keys of [Match] narrow it, which
// it cannot match: the documents give no link's address, driver or other properties.
type Unit struct {
	Name  string
	Match NameMatch
	// VRF is the unit's own VRF=, at VRFPath in the Network document, and Device tells a VRF
	// device's unit.
	VRF, VRFPath string
	Device       bool
	// Narrowed tells a unit whose [Match] holds other keys, which may match a link its Name=
	// matches, or not.
	Narrowed bool
}

// vrfLinks checks the VRF documents against the Network documents of byKind, which name the
// links, where the VRF's rules cannot, and returns the VRF of each Network. A VRF holds the links
// its document's interfaces match and those a Network document's Name= matches whose own VRF=
// names it, whether a document of the VRF exists or not. A Network document is in the VRF of the
// links it may be the first to match, and vrfLinks refuses unless all links it may match are in
// that VRF, so that VRF= on the Network puts in the VRF the links that the VRF holds, and only
// those. It refuses a link two VRFs may hold, a literal interface of a VRF no Network matches by
// Name= alone, and a VRF device whose first Network is not its own. Without VRF documents and
// VRF= it checks nothing and puts every link in the default VRF, so that a router image without
// VRFs takes the Network documents it took before them.
//
// It goes on past a refusal to the end of each of its steps and returns every refusal of the
// first step that refuses, joined: the VRFs' members and the units, then the Networks that may be
// the first to match each member's links, then the VRF of each Network. A step reads what the
// steps before it found, which a refusal leaves wrong.
func vrfLinks(vrfs []Named[VRFSpec], links []string, byKind map[string]any) (Membership, error) {
	var none Membership
	if len(vrfs) == 0 && !anyNetworkVRF(byKind) {
		return none, nil
	}
	var errs []error
	var members []Member
	for _, v := range vrfs {
		what := "VRF " + v.Name
		// The VRF's rules refuse these names and tables. networkd would refuse a .netdev of either
		// name, which is no interface name, and of table 0, so the guards keep units it cannot
		// load out of the output.
		if v.Name == DefaultVRF || len(v.Name) > 15 {
			return none, InternalErrorf("%s: a VRF is named as its device, in 15 characters at most, and not %s", what, DefaultVRF)
		}
		if v.Spec.Table == 0 {
			return none, InternalErrorf("%s: table 0 is no table of a VRF", what)
		}
		if slices.Contains(links, v.Name) {
			errs = append(errs, refusals.Errorf("VRF", v.Name, "metadata.name", "a Network document names the link %s, the VRF's device", v.Name))
		}
		for j, i := range v.Spec.Interfaces {
			if !InterfaceName.MatchString(i) {
				return none, InternalErrorf("%s: %q is no interface name or glob", what, i)
			}
			members = append(members, Member{VRF: v.Name, Glob: i, Text: i, Path: fmt.Sprintf("spec.interfaces[%d]", j)})
		}
	}
	for _, m := range members {
		for _, v := range vrfs {
			if ok, _ := path.Match(m.Glob, v.Name); ok {
				errs = append(errs, refusals.Errorf("VRF", m.VRF, m.Path, "VRF %s's %s covers the device of VRF %s", m.VRF, m.Glob, v.Name))
			}
		}
	}
	units, err := networkUnits(vrfs, byKind)
	if err != nil {
		return none, errors.Join(append(errs, err)...)
	}
	for _, u := range units {
		if u.VRF != "" {
			for _, w := range u.Match.Positives() {
				members = append(members, Member{VRF: u.VRF, Glob: w.Glob, Text: w.Text, Network: u.Name, Path: cmp.Or(w.Path, "spec.match"), VRFPath: u.VRFPath})
			}
		}
	}
	errs = append(errs, checkVRFMembers(members)...)
	for _, v := range vrfs {
		if u, own := Candidates(units, v.Name)[0], VRFUnitPrefix+v.Name; u.Name != own {
			if u.Narrowed {
				errs = append(errs, refusals.Errorf("Network", u.Name, "metadata.name", "Network %s may match the device of VRF %s before %s, should its other [Match] keys match it; rename it to sort after %s", u.Name, v.Name, own, own))
			} else {
				errs = append(errs, refusals.Errorf("Network", u.Name, "metadata.name", "Network %s matches the device of VRF %s before %s; rename it to sort after %s", u.Name, v.Name, own, own))
			}
		}
	}
	for _, m := range members {
		if cs := Candidates(units, m.Glob); m.Network == "" && isLiteral(m.Glob) && (len(cs) == 0 || cs[len(cs)-1].Narrowed) {
			errs = append(errs, refusals.Errorf("VRF", m.VRF, m.Path, "VRF %s names the interface %s, which no Network document's [Match] Name= matches without other [Match] keys; VRF= reaches a link through its Network document, so give it one", m.VRF, m.Glob))
		}
	}
	if len(errs) > 0 {
		return none, errors.Join(errs...)
	}
	networks, errs := firstNetworks(units, members)
	if len(errs) > 0 {
		return none, errors.Join(errs...)
	}
	if errs := checkNetworkVRFs(units, members, networks); len(errs) > 0 {
		return none, errors.Join(errs...)
	}
	return Membership{Networks: networks, Units: units, Members: members}, nil
}

// anyNetworkVRF reports whether a Network document of byKind gives VRF=, or a value of VRF= that
// NetworkVRF refuses.
func anyNetworkVRF(byKind map[string]any) bool {
	docs, _ := byKind["Network"].([]any)
	for _, d := range docs {
		doc, _ := d.(map[string]any)
		spec, _ := doc["spec"].(map[string]any)
		if vrf, _, err := NetworkVRF("", spec); vrf != "" || err != nil {
			return true
		}
	}
	return false
}

// Units are the .network units networkd reads of the model's Network and VRF documents, as
// networkUnits finds them.
func (d Model) Units() ([]Unit, error) {
	return networkUnits(d.VRFs, d.byKind)
}

// networkUnits are the .network units networkd reads: the Network documents of byKind and the
// units of the VRFs' devices, sorted by file name as networkd sorts them.
func networkUnits(vrfs []Named[VRFSpec], byKind map[string]any) ([]Unit, error) {
	var units []Unit
	var errs []error
	docs, _ := byKind["Network"].([]any)
	for _, d := range docs {
		doc, _ := d.(map[string]any)
		name, err := DocumentName("Network", doc)
		if err != nil {
			return nil, err
		}
		spec, _ := doc["spec"].(map[string]any)
		vrf, vrfPath, err := NetworkVRF(name, spec)
		match, matchErr := parseNameMatch(name, spec)
		if err != nil || matchErr != nil {
			errs = append(errs, err, matchErr)
			continue
		}
		narrowed := otherMatchKeys(spec)
		if len(match.Words) == 0 && !narrowed {
			// networkd ignores a .network whose [Match] is empty.
			continue
		}
		units = append(units, Unit{Name: name, Match: match, VRF: vrf, VRFPath: vrfPath, Narrowed: narrowed})
	}
	for _, v := range vrfs {
		match := NameMatch{Words: []NameWord{{Glob: v.Name, Text: v.Name}}}
		units = append(units, Unit{Name: VRFUnitPrefix + v.Name, Match: match, Device: true})
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	slices.SortStableFunc(units, func(a, b Unit) int { return strings.Compare(a.Name+".network", b.Name+".network") })
	return units, nil
}

// otherMatchKeys reports whether a Network document's spec gives [Match] keys besides Name=. An
// empty value resets a key, as systemd reads it, so a key whose last value is empty is not given.
func otherMatchKeys(spec map[string]any) bool {
	given := map[string]bool{}
	for _, s := range OneOrMany(spec["match"]) {
		section, _ := s.(map[string]any)
		for k, v := range section {
			if k == "name" {
				continue
			}
			for _, value := range OneOrMany(v) {
				if value == nil {
					continue
				}
				text, isText := value.(string)
				given[k] = !isText || strings.Trim(text, UnitWhitespace) != ""
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

// Candidates are the sorted units that may be the first to match the link: those whose Name=
// matches it, up to the first that other [Match] keys do not narrow.
func Candidates(units []Unit, link string) []Unit {
	var cs []Unit
	for _, u := range units {
		if u.Match.Matches(link) {
			cs = append(cs, u)
			if !u.Narrowed {
				break
			}
		}
	}
	return cs
}

// checkVRFMembers refuses globs of two VRFs that may match one link, each pair of them.
func checkVRFMembers(members []Member) []error {
	var errs []error
	for i, a := range members {
		for _, b := range members[i+1:] {
			if a.VRF == b.VRF || !GlobsOverlap(a.Glob, b.Glob) {
				continue
			}
			switch {
			case a.Network == "" && b.Network == "" && (isLiteral(a.Glob) || isLiteral(b.Glob)):
				link := a.Glob
				if !isLiteral(link) {
					link = b.Glob
				}
				errs = append(errs, refusals.Errorf("VRF", b.VRF, b.Path, "the link %s is in VRF %s and in VRF %s", link, a.VRF, b.VRF))
			case a.Network == "" && b.Network == "":
				errs = append(errs, refusals.Errorf("VRF", b.VRF, b.Path, "VRF %s's %s and VRF %s's %s may hold one link", a.VRF, a.Glob, b.VRF, b.Glob))
			case a.Network == "" || b.Network == "":
				n, v := a, b
				if n.Network == "" {
					n, v = b, a
				}
				errs = append(errs, refusals.Errorf("Network", n.Network, n.VRFPath, "Network %s gives its links VRF=%s, but VRF %s holds them", n.Network, n.VRF, v.VRF))
			default:
				errs = append(errs, refusals.Errorf("Network", b.Network, b.Path, "Network %s gives VRF=%s and Network %s VRF=%s to links both may match", a.Network, a.VRF, b.Network, b.VRF))
			}
		}
	}
	return errs
}

// firstNetworks are the Network documents that may be the first unit to match a member's links,
// by name, with the member's VRF. Of a literal member those are its candidates. Of a glob it is
// each unit that may match some of its links, up to the first unit that matches all of them,
// unless the units before it match all of the links its words share with the glob. A unit that
// other [Match] keys narrow matches no link for certain, so it neither ends the units nor hides
// the links from later ones. It refuses each member at the first unit it refuses for the member.
func firstNetworks(units []Unit, members []Member) (map[string]string, []error) {
	networks := map[string]string{}
	var errs []error
	take := func(u Unit, m Member, after bool) error {
		if u.Device {
			return nil
		}
		if m.Network != "" && u.Name != m.Network && u.VRF != m.VRF {
			return shadowsVRF(units, u, m, after)
		}
		if other, ok := networks[u.Name]; ok && other != m.VRF {
			in := []string{other, m.VRF}
			slices.Sort(in)
			return refusals.Errorf("Network", u.Name, "spec.match", "Network %s matches links of %s; the links of one Network are in one VRF", u.Name, vrfList(in))
		}
		networks[u.Name] = m.VRF
		return nil
	}
	for _, m := range members {
		after := false
		if isLiteral(m.Glob) {
			for _, u := range Candidates(units, m.Glob) {
				if err := take(u, m, after); err != nil {
					errs = append(errs, err)
					break
				}
				after = after || u.Name == m.Network
			}
			continue
		}
		var before []NameMatch
		for _, u := range units {
			if u.Match.MayMatch(m.Glob) && !shadowed(u.Match, m.Glob, before) {
				if err := take(u, m, after); err != nil {
					errs = append(errs, err)
					break
				}
			}
			after = after || u.Name == m.Network
			if u.Narrowed {
				continue
			}
			if u.Match.Covers(m.Glob) {
				break
			}
			before = append(before, u.Match)
		}
	}
	return networks, errs
}

// shadowsVRF is the refusal of the unit u, of no VRF= of m's VRF, that may match links of the
// member m of a Network's own VRF= first: before that Network, or after it when the Network's
// other [Match] keys or negated words may leave the links to u. VRF= on u would put the links in
// the VRF where networkd does not, so the validator refuses rather than give u one.
func shadowsVRF(units []Unit, u Unit, m Member, after bool) error {
	var owner Unit
	if i := slices.IndexFunc(units, func(o Unit) bool { return o.Name == m.Network }); i >= 0 {
		owner = units[i]
	}
	// A member of no text stands for the links of a Name= of negated words alone, or of none.
	links, them := "links of "+m.Text, "them"
	switch {
	case isLiteral(m.Glob):
		links, them = m.Glob, "it"
	case m.Text != "":
	case len(owner.Match.Words) == 0:
		links = "links Network " + m.Network + " may match"
	case after && !owner.Narrowed:
		links = "links Network " + m.Network + "'s Name= negates"
	default:
		links = "links Network " + m.Network + "'s Name= does not negate"
	}
	if after {
		when := "Network " + m.Network + " does not"
		if owner.Narrowed {
			when = "the other [Match] keys of Network " + m.Network + " do not"
		}
		return refusals.Errorf("Network", u.Name, "spec.match", "Network %s matches %s when %s, and networkd then does not apply %s's VRF=%s to %s; give %s VRF=%s", u.Name, links, when, m.Network, m.VRF, them, u.Name, m.VRF)
	}
	first, applies := "is the first", "never applies"
	if u.Narrowed {
		first, applies = "may be the first", "may not apply"
	}
	// Sorted after a Network whose other [Match] keys may not match, u may still match the links.
	fix := fmt.Sprintf("give %s VRF=%s, rename it to sort after %s, or drop %s's VRF=", u.Name, m.VRF, m.Network, m.Network)
	if owner.Narrowed {
		fix = fmt.Sprintf("give %s VRF=%s or drop %s's VRF=", u.Name, m.VRF, m.Network)
	}
	return refusals.Errorf("Network", u.Name, "spec.match", "Network %s %s to match %s, so networkd %s Network %s's VRF=%s to %s; %s", u.Name, first, links, applies, m.Network, m.VRF, them, fix)
}

// shadowed reports whether units before one of Name= m match every link that m's words share
// with the glob, so that m matches none of them first.
func shadowed(m NameMatch, glob string, before []NameMatch) bool {
	for _, w := range m.Positives() {
		if GlobsOverlap(w.Glob, glob) && !slices.ContainsFunc(before, func(b NameMatch) bool { return b.Covers(w.Glob) }) {
			return false
		}
	}
	return true
}

// checkNetworkVRFs refuses a Network of a VRF that may match links outside the VRF, or whose own
// VRF= names another VRF, at the first fault of each Network.
func checkNetworkVRFs(units []Unit, members []Member, networks map[string]string) []error {
	var errs []error
	for _, u := range units {
		if err := checkNetworkVRF(u, members, networks); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// checkNetworkVRF refuses the unit u, of a Network of a VRF, that may match links outside the
// VRF, or whose own VRF= names another VRF.
func checkNetworkVRF(u Unit, members []Member, networks map[string]string) error {
	vrf, ok := networks[u.Name]
	if !ok {
		return nil
	}
	if u.VRF != "" && u.VRF != vrf {
		return refusals.Errorf("Network", u.Name, u.VRFPath, "Network %s gives its links VRF=%s, but VRF %s holds them", u.Name, u.VRF, vrf)
	}
	for _, w := range u.Match.Positives() {
		if slices.ContainsFunc(members, func(m Member) bool { return m.VRF == vrf && globContains(m.Glob, w.Glob) }) {
			continue
		}
		switch {
		case len(u.Match.Words) == 0:
			return refusals.Errorf("Network", u.Name, "spec.match", "Network %s matches by its other [Match] keys alone, as its Name= holds no word networkd takes, so it may match links of VRF %s and links outside it; name its links", u.Name, vrf)
		case w.Text == "":
			return refusals.Errorf("Network", u.Name, "spec.match", "Network %s matches every link its Name= does not negate, links of VRF %s and links outside it; name its links", u.Name, vrf)
		case isLiteral(w.Glob):
			in := []string{"", vrf}
			for _, m := range members {
				if ok, _ := path.Match(m.Glob, w.Glob); ok {
					in[0] = m.VRF
				}
			}
			slices.Sort(in)
			return refusals.Errorf("Network", u.Name, "spec.match", "Network %s matches links of %s; the links of one Network are in one VRF", u.Name, vrfList(in))
		default:
			return refusals.Errorf("Network", u.Name, w.Path, "Network %s's Name=%s may match links of VRF %s and links outside it; name its links", u.Name, w.Text, vrf)
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

// NetworkVRF is the VRF= the [Network] sections of the Network document name give, "" for none,
// and the field of its last value, such as spec.network.vrf. An empty VRF= drops the one before
// it, as systemd reads it.
func NetworkVRF(name string, spec map[string]any) (string, string, error) {
	vrf, at := "", ""
	for si, s := range OneOrMany(spec["network"]) {
		section, _ := s.(map[string]any)
		values := ItemPath("spec.network", spec["network"], si) + ".vrf"
		for vi, v := range OneOrMany(section["vrf"]) {
			if v == nil {
				continue
			}
			if v == "" {
				vrf, at = "", ""
				continue
			}
			value, ok := v.(string)
			if !ok || !PlainInterface.MatchString(value) || vrf != "" && value != vrf {
				return "", "", refusals.Errorf("Network", name, ItemPath(values, section["vrf"], vi), "VRF= names one VRF by its name, not %q", fmt.Sprint(v))
			}
			vrf, at = value, ItemPath(values, section["vrf"], vi)
		}
	}
	return vrf, at, nil
}
