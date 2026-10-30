package main

import (
	"fmt"
	"maps"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// frrFile is the file renderFRR writes.
const frrFile = "frr.conf"

// The names frr.conf gives what the documents declare, each kind with a prefix of its own, so a
// peer group named after a BGPPeer never takes the name of an interface or of another list.
const (
	peerGroupPrefix  = "peer-"
	prefixListPrefix = "pl-"
	communityPrefix  = "cl-"
	policyPrefix     = "rp-"
	// acceptAll is the route-map of a peer's family without a policy.
	acceptAll = "accept-all"
	// bfdProfile holds the BFD timers of the default VRF's BGP document; another VRF's profile
	// is bfdProfile-<vrf>.
	bfdProfile = "chalkos"
)

// renderFRR writes FRR's whole config to out/frr.conf: a BGP instance per BGP document, of the
// default VRF or of another, with a peer group per BGPPeer of its VRF, the PrefixList,
// CommunityList and RoutingPolicy documents as prefix-lists, community-lists and route-maps, and a
// BFD profile of each instance's timers. A peer's password is a placeholder,
// @secret_<secret>.<key>@, that frr.service fills in from its credentials. A node without a BGP
// document runs no BGP.
func renderFRR(in map[string]any, out string) error {
	d, err := readRouterDocuments(in)
	if err != nil {
		return err
	}
	node, _ := in["node"].(map[string]any)
	name, _ := node["name"].(string)
	text, err := frrConfig(d, name)
	if err != nil {
		return err
	}
	return writeFile(out, frrFile, []byte(text))
}

// hostName is a node name FRR's hostname takes.
var hostName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,62})$`)

func frrConfig(d routerDocuments, node string) (string, error) {
	var b strings.Builder
	b.WriteString("! FRR's config, rendered by chalkos-render frr. frr.service fills in the passwords, and\n" +
		"! frr-reload.py applies the changes.\n" +
		"frr defaults traditional\n")
	if hostName.MatchString(node) {
		b.WriteString("hostname " + node + "\n")
	}
	b.WriteString("log syslog informational\nservice integrated-vtysh-config\n!\n")
	switch {
	case len(d.BGP) == 0 && len(d.Peers) > 0:
		return "", fmt.Errorf("the config holds BGPPeer documents but no BGP document")
	case len(d.BGP) == 0:
		b.WriteString("end\n")
		return b.String(), nil
	}
	c, err := newFRR(d)
	if err != nil {
		return "", err
	}
	c.prefixLists(&b)
	c.communityLists(&b)
	c.routeMaps(&b)
	for _, in := range c.instances {
		c.router(&b, in)
	}
	c.bfd(&b)
	b.WriteString("end\n")
	return b.String(), nil
}

// frr is what frr.conf is rendered from once the documents passed their checks.
type frr struct {
	// instances are the BGP documents, the default VRF's first and the others by VRF.
	instances   []*instance
	peers       []named[peerSpec]
	prefixes    []named[prefixListSpec]
	communities []named[communityListSpec]
	policies    []named[routingPolicySpec]
	// families are the PrefixList documents' families, and large whether a CommunityList holds
	// large communities, by name.
	families map[string]string
	large    map[string]bool
	// entries are each PrefixList's entries as frr.conf writes them, by name.
	entries map[string][]string
	// policyFamily is each RoutingPolicy's family, that of its PrefixLists; "" for a policy that
	// matches none.
	policyFamily map[string]string
	// neighbours are the interfaces or the address of each peer, by peer name.
	neighbours map[string][]neighbour
	// links are the links the Network documents name, and membership tells the VRF networkd puts
	// a link in.
	links      []string
	membership vrfMembership
	// vrfs are the VRF documents.
	vrfs []named[vrfSpec]
	// prependAS is the AS number the prepends of each RoutingPolicy with one repeat, by name.
	prependAS map[string]uint32
}

// An instance is a BGP document: the BGP instance of one VRF, and the peers in that VRF.
type instance struct {
	name, vrf string
	spec      bgpSpec
	peers     []named[peerSpec]
}

// served is the instance of the VRF, nil when no BGP document serves it.
func (c *frr) served(vrf string) *instance {
	for _, in := range c.instances {
		if in.vrf == vrf {
			return in
		}
	}
	return nil
}

// leak is what the instance imports of the family, nil for nothing.
func (in *instance) leak(family string) *leakSpec {
	if family == "ipv4" {
		return in.spec.Import.IPv4
	}
	return in.spec.Import.IPv6
}

// profile is the BFD profile of the instance's timers: bfdProfile for the default VRF's, and
// bfdProfile-<vrf> for another's.
func (in *instance) profile() string {
	if in.vrf == defaultVRF {
		return bfdProfile
	}
	return bfdProfile + "-" + in.vrf
}

// A neighbour is one session of a peer: on the link of an interface, or with an address.
type neighbour struct {
	name   string
	onLink bool
}

// community is a community as FRR writes it, and whether it is a large one.
func community(c string) (string, bool, error) {
	parts := strings.Split(c, ":")
	bits := 16
	if len(parts) == 3 {
		bits = 32
	}
	if len(parts) < 2 || len(parts) > 3 {
		return "", false, fmt.Errorf("%q is no community <AS>:<value> or large community <AS>:<value>:<value>", c)
	}
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, bits)
		if err != nil {
			return "", false, fmt.Errorf("%q is no community: its parts are numbers of %d bits", c, bits)
		}
		parts[i] = strconv.FormatUint(n, 10)
	}
	return strings.Join(parts, ":"), len(parts) == 3, nil
}

// asPathRegex is a RoutingPolicy term's asPath as the schema takes it, without the spaces at its
// ends or in a row that FRR's parser would drop.
var asPathRegex = regexp.MustCompile(`^[0-9_^$.*+?()|{},-]+( [0-9_^$.*+?()|{},-]+)*$`)

// asPathInterval is an interval {m}, {m,} or {m,n} at the start of a string.
var asPathInterval = regexp.MustCompile(`^\{([0-9]{1,3})(,([0-9]{1,3})?)?\}`)

// checkASPath refuses an AS path's regular expression that bgpd's regcomp, glibc's POSIX ERE with
// each _ standing for the start, the end or a separator of the path, refuses: bgpd would drop the
// as-path list, and the term would match no route. A repetition follows something it repeats,
// not the start, a (, a |, a ^ or a $, and parentheses pair up. It also refuses a few glibc
// takes, an interval without its first number among them, and what Go's POSIX syntax refuses.
func checkASPath(r string) error {
	depth, repeatable := 0, false
	for i := 0; i < len(r); i++ {
		switch c := r[i]; c {
		case '(':
			depth++
			repeatable = false
		case ')':
			if depth == 0 {
				return fmt.Errorf("the ) at %d closes no (", i+1)
			}
			depth--
			repeatable = true
		case '|', '^', '$':
			repeatable = false
		case '*', '+', '?', '{':
			if !repeatable {
				return fmt.Errorf("the %c at %d repeats nothing", c, i+1)
			}
			if c == '{' {
				m := asPathInterval.FindStringSubmatch(r[i:])
				if m == nil {
					return fmt.Errorf("the { at %d opens no interval {m}, {m,} or {m,n}", i+1)
				}
				if lo, _ := strconv.Atoi(m[1]); m[3] != "" {
					if hi, _ := strconv.Atoi(m[3]); lo > hi {
						return fmt.Errorf("the interval %s at %d ends below its start", m[0], i+1)
					}
				}
				i += len(m[0]) - 1
			}
		default:
			repeatable = true
		}
	}
	if depth > 0 {
		return fmt.Errorf("%d ( are not closed", depth)
	}
	_, err := regexp.CompilePOSIX(strings.ReplaceAll(r, "_", "(^|[,{}() ]|$)"))
	return err
}

// credential is a secret's key as a credential name of frr.service, which holds letters, digits,
// dots, underscores and dashes alone.
var credential = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// description is a BGPPeer's description as the schema takes it, without the spaces at its ends
// or in a row that FRR's parser would drop, and without an @, so that it never holds a password
// placeholder the credentials helper would fill in for every reader of the config; 80 characters
// at most.
var description = regexp.MustCompile(`^[!-?A-~]+( [!-?A-~]+)*$`)

// plainInterface is an interface's name without a glob.
var plainInterface = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,15}$`)

// newFRR checks the documents as the schemas and rules do, should one bypass them, and expands
// each peer's interfaces against the links the Network documents name, which are in its VRF.
func newFRR(d routerDocuments) (*frr, error) {
	c := &frr{
		peers:        d.Peers,
		prefixes:     d.Prefixes,
		communities:  d.Communities,
		policies:     d.RoutingPolicies,
		families:     map[string]string{},
		large:        map[string]bool{},
		entries:      map[string][]string{},
		policyFamily: map[string]string{},
		neighbours:   map[string][]neighbour{},
		links:        d.Links,
		membership:   d.Membership,
		vrfs:         d.VRFs,
		prependAS:    map[string]uint32{},
	}
	for _, g := range d.BGP {
		in, err := newInstance(g, d.VRFs)
		if err != nil {
			return nil, err
		}
		if other := c.served(in.vrf); other != nil {
			return nil, fmt.Errorf("BGP %s and BGP %s both serve the VRF %s; a VRF has one BGP instance", other.name, g.Name, in.vrf)
		}
		c.instances = append(c.instances, in)
	}
	// FRR takes the default VRF's instance first.
	slices.SortStableFunc(c.instances, func(a, b *instance) int { return strings.Compare(vrfOrder(a.vrf), vrfOrder(b.vrf)) })
	for _, l := range d.Prefixes {
		entries, err := prefixEntries(l.Spec)
		if err != nil {
			return nil, fmt.Errorf("PrefixList %s: %w", l.Name, err)
		}
		c.families[l.Name] = l.Spec.Family
		c.entries[l.Name] = entries
	}
	for _, l := range d.Communities {
		if len(l.Spec.Communities) == 0 {
			return nil, fmt.Errorf("CommunityList %s holds no community", l.Name)
		}
		seen := map[string]bool{}
		for i, v := range l.Spec.Communities {
			v, large, err := community(v)
			if err != nil {
				return nil, fmt.Errorf("CommunityList %s: %w", l.Name, err)
			}
			if i > 0 && large != c.large[l.Name] {
				return nil, fmt.Errorf("CommunityList %s holds standard and large communities", l.Name)
			}
			if seen[v] {
				return nil, fmt.Errorf("CommunityList %s holds %s twice", l.Name, v)
			}
			seen[v] = true
			c.large[l.Name] = large
		}
	}
	for _, p := range d.RoutingPolicies {
		if err := c.checkPolicy(p); err != nil {
			return nil, fmt.Errorf("RoutingPolicy %s: %w", p.Name, err)
		}
	}
	for _, in := range c.instances {
		if err := c.checkNetworks(in); err != nil {
			return nil, err
		}
		if err := c.checkImports(in); err != nil {
			return nil, err
		}
	}
	// Who names each session of a VRF, so two peers never name one, and who names each neighbour
	// in any VRF, so none takes a peer group's name.
	owner, sessions := map[string]string{}, map[string]string{}
	for _, p := range d.Peers {
		vrf := vrfOf(p.Spec.VRF)
		in := c.served(vrf)
		if in == nil {
			return nil, fmt.Errorf("BGPPeer %s runs in the VRF %s, which no BGP document serves", p.Name, vrf)
		}
		ns, err := c.peerNeighbours(p, vrf)
		if err != nil {
			return nil, fmt.Errorf("BGPPeer %s: %w", p.Name, err)
		}
		for _, n := range ns {
			key := vrf + " " + n.name
			if other, ok := owner[key]; ok {
				return nil, fmt.Errorf("BGPPeer %s and BGPPeer %s both name %s in the VRF %s; give each session one peer", other, p.Name, n.name, vrf)
			}
			owner[key] = p.Name
			sessions[n.name] = p.Name
		}
		c.neighbours[p.Name] = ns
		in.peers = append(in.peers, p)
	}
	for _, p := range d.Peers {
		if peer, ok := sessions[peerGroupPrefix+p.Name]; ok {
			return nil, fmt.Errorf("BGPPeer %s names %s%s, the peer group of BGPPeer %s; rename the document or the interface", peer, peerGroupPrefix, p.Name, p.Name)
		}
	}
	if err := c.prepends(); err != nil {
		return nil, err
	}
	return c, nil
}

// newInstance checks a BGP document's VRF, which a VRF document declares unless it is the default
// one, and its settings.
func newInstance(g named[bgpSpec], vrfs []named[vrfSpec]) (*instance, error) {
	b := g.Spec
	in := &instance{name: g.Name, vrf: vrfOf(b.VRF), spec: b}
	what := "BGP " + g.Name
	if in.vrf != defaultVRF && !slices.ContainsFunc(vrfs, func(v named[vrfSpec]) bool { return v.Name == in.vrf }) {
		return nil, fmt.Errorf("%s: the VRF %q has no VRF document", what, in.vrf)
	}
	if b.ASN == 0 {
		return nil, fmt.Errorf("%s: asn 0 is no AS number", what)
	}
	if a, err := netip.ParseAddr(b.RouterID); err != nil || !a.Is4() || a.IsUnspecified() || a.IsMulticast() || a == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return nil, fmt.Errorf("%s: routerID %q is no IPv4 unicast address", what, b.RouterID)
	}
	for _, m := range []struct {
		what      string
		v, lo, hi int
	}{
		{"multipath.ebgp", b.Multipath.EBGP, 1, 64}, {"multipath.ibgp", b.Multipath.IBGP, 1, 64},
		{"bfd.receiveMs", b.BFD.ReceiveMs, 10, 60000}, {"bfd.transmitMs", b.BFD.TransmitMs, 10, 60000},
		{"bfd.multiplier", b.BFD.Multiplier, 2, 255},
	} {
		if m.v < m.lo || m.v > m.hi {
			return nil, fmt.Errorf("%s: %s %d lies outside %d to %d", what, m.what, m.v, m.lo, m.hi)
		}
	}
	return in, nil
}

// checkNetworks refuses a network of an instance that is no address range, two networks of one
// range, a default route in a VRF, whose table always holds one, and a policy of another family.
func (c *frr) checkNetworks(in *instance) error {
	announced := map[netip.Prefix]string{}
	for _, n := range in.spec.Networks {
		p, err := netip.ParsePrefix(n.Prefix)
		if err != nil {
			return fmt.Errorf("BGP %s: network %q is no address range", in.name, n.Prefix)
		}
		if other, ok := announced[p.Masked()]; ok {
			return fmt.Errorf("BGP %s: networks %s and %s both announce %s; give each range one network", in.name, other, n.Prefix, p.Masked())
		}
		if p.Bits() == 0 && in.vrf != defaultVRF {
			return fmt.Errorf("BGP %s: network %s of VRF %s would always be announced, as the VRF's table ends in an unreachable default route; use a peer family's defaultOriginate", in.name, n.Prefix, in.vrf)
		}
		announced[p.Masked()] = n.Prefix
		if n.Policy != nil {
			if err := c.policyOfFamily(*n.Policy, familyOf(p.Addr())); err != nil {
				return fmt.Errorf("BGP %s: network %s: %w", in.name, n.Prefix, err)
			}
		}
	}
	return nil
}

// checkImports refuses an import of an instance that names no VRF, its own VRF, a VRF twice or one
// that no BGP document serves, an import into or out of a VRF with a conntrack zone, and a policy
// of another family. Every VRF it names is then an instance's, whose name the documents' checks
// took.
func (c *frr) checkImports(in *instance) error {
	for _, f := range []struct {
		family string
		leak   *leakSpec
	}{{"ipv4", in.spec.Import.IPv4}, {"ipv6", in.spec.Import.IPv6}} {
		if f.leak == nil {
			continue
		}
		what := fmt.Sprintf("BGP %s: import.%s", in.name, f.family)
		if len(f.leak.VRFs) == 0 {
			return fmt.Errorf("%s names no VRF", what)
		}
		seen := map[string]bool{}
		for _, v := range f.leak.VRFs {
			switch {
			case v == in.vrf:
				return fmt.Errorf("%s names its own VRF %s", what, v)
			case seen[v]:
				return fmt.Errorf("%s names the VRF %s twice", what, v)
			case c.served(v) == nil:
				return fmt.Errorf("%s names the VRF %q, which no BGP document serves", what, v)
			}
			for _, end := range []string{v, in.vrf} {
				if c.zoned(end) {
					return fmt.Errorf("%s names the VRF %s, and VRF %s has a conntrack zone, so the replies of leaked connections would land in another zone; drop the zone or the import", what, v, end)
				}
			}
			seen[v] = true
		}
		if f.leak.Policy != nil {
			if err := c.policyOfFamily(*f.leak.Policy, f.family); err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
		}
	}
	return nil
}

// zoned reports whether the VRF has a conntrack zone of its own; the default VRF has none.
func (c *frr) zoned(vrf string) bool {
	return slices.ContainsFunc(c.vrfs, func(v named[vrfSpec]) bool { return v.Name == vrf && v.Spec.ConntrackZone != nil })
}

// prepends finds the AS number each RoutingPolicy's prepend repeats: that of the BGP instances
// whose networks, peers or imports use the policy. A route-map serves every instance, so a policy
// with a prepend that instances of two AS numbers use is refused; one that no instance uses
// repeats the first instance's.
func (c *frr) prepends() error {
	uses := map[string]map[uint32]bool{}
	use := func(policy *string, asn uint32) {
		if policy == nil {
			return
		}
		if uses[*policy] == nil {
			uses[*policy] = map[uint32]bool{}
		}
		uses[*policy][asn] = true
	}
	for _, in := range c.instances {
		asn := in.spec.ASN
		for _, n := range in.spec.Networks {
			use(n.Policy, asn)
		}
		for _, fam := range []string{"ipv4", "ipv6"} {
			if l := in.leak(fam); l != nil {
				use(l.Policy, asn)
			}
		}
		for _, p := range in.peers {
			for _, f := range []*peerFamily{p.Spec.Families.IPv4, p.Spec.Families.IPv6} {
				if f != nil {
					use(f.Import, asn)
					use(f.Export, asn)
				}
			}
		}
	}
	for _, p := range c.policies {
		prepends := false
		for _, t := range p.Spec.Terms {
			prepends = prepends || t.Then.Prepend != nil
		}
		if !prepends {
			continue
		}
		switch asns := slices.Sorted(maps.Keys(uses[p.Name])); len(asns) {
		case 0:
			c.prependAS[p.Name] = c.instances[0].spec.ASN
		case 1:
			c.prependAS[p.Name] = asns[0]
		default:
			return fmt.Errorf("RoutingPolicy %s prepends the AS number, and BGP instances of AS %d and %d use it; give each AS a policy of its own", p.Name, asns[0], asns[1])
		}
	}
	return nil
}

func familyOf(a netip.Addr) string {
	if a.Is4() {
		return "ipv4"
	}
	return "ipv6"
}

// checkPolicy checks a RoutingPolicy's terms and notes its family.
func (c *frr) checkPolicy(p named[routingPolicySpec]) error {
	if len(p.Spec.Terms) > 6000 {
		return fmt.Errorf("%d terms are more than the 6000 a route-map holds", len(p.Spec.Terms))
	}
	if p.Spec.Default != "accept" && p.Spec.Default != "reject" {
		return fmt.Errorf("default %q is neither accept nor reject", p.Spec.Default)
	}
	for i, t := range p.Spec.Terms {
		fail := func(format string, args ...any) error {
			return fmt.Errorf("term %d: %s", i+1, fmt.Sprintf(format, args...))
		}
		if l := t.Match.PrefixList; l != nil {
			f, ok := c.families[*l]
			if !ok {
				return fail("the PrefixList %s does not exist", *l)
			}
			if have := c.policyFamily[p.Name]; have != "" && have != f {
				return fail("it matches PrefixLists of both families")
			}
			c.policyFamily[p.Name] = f
		}
		if l := t.Match.CommunityList; l != nil {
			if _, ok := c.large[*l]; !ok {
				return fail("the CommunityList %s does not exist", *l)
			}
		}
		if r := t.Match.ASPath; r != nil {
			if len(*r) > 200 || !asPathRegex.MatchString(*r) {
				return fail("asPath %q holds more than digits, spaces and _^$.*+?()|{},- (200 at most, a space between two others)", *r)
			}
			if err := checkASPath(*r); err != nil {
				return fail("asPath %q is no regular expression bgpd compiles: %v", *r, err)
			}
		}
		th := t.Then
		switch th.Action {
		case "accept":
		case "reject":
			if th.LocalPref != nil || th.MED != nil || len(th.AddCommunities) > 0 || len(th.RemoveCommunities) > 0 || th.Prepend != nil {
				return fail("it rejects and sets attributes")
			}
		default:
			return fail("action %q is neither accept nor reject", th.Action)
		}
		for _, v := range append(slices.Clone(th.AddCommunities), th.RemoveCommunities...) {
			if _, _, err := community(v); err != nil {
				return fail("%v", err)
			}
		}
		if th.Prepend != nil && (*th.Prepend < 1 || *th.Prepend > 10) {
			return fail("prepend %d lies outside 1 to 10", *th.Prepend)
		}
	}
	return nil
}

// policyOfFamily refuses a policy the config lacks, or one whose PrefixLists are of another
// family than f.
func (c *frr) policyOfFamily(policy, f string) error {
	if !slices.ContainsFunc(c.policies, func(p named[routingPolicySpec]) bool { return p.Name == policy }) {
		return fmt.Errorf("the RoutingPolicy %s does not exist", policy)
	}
	if have := c.policyFamily[policy]; have != "" && have != f {
		return fmt.Errorf("the RoutingPolicy %s matches PrefixLists of %s, not %s", policy, have, f)
	}
	return nil
}

// peerNeighbours are a peer's sessions: its address, its interface, which is a link of its VRF, or
// the links of its VRF its interfaces name, globs matching the links the Network documents name.
// A glob skips the links of other VRFs, those of no VRF for a peer of a VRF and those of a VRF
// for a peer of the default one, and is refused when it matches only those. An interface or glob
// of a VRF's device is refused.
func (c *frr) peerNeighbours(p named[peerSpec], vrf string) ([]neighbour, error) {
	s := p.Spec
	if !s.namesOneNeighbour() {
		return nil, fmt.Errorf("it names not exactly one of address, interface and interfaces")
	}
	if s.RemoteAS == "" {
		return nil, fmt.Errorf("it has no remoteAS")
	}
	if s.Password != nil && (!credential.MatchString(s.Password.SecretRef.Name) || !credential.MatchString(s.Password.SecretRef.Key)) {
		return nil, fmt.Errorf("the password's Secret %q and key %q are letters, digits, '.', '_' and '-' alone", s.Password.SecretRef.Name, s.Password.SecretRef.Key)
	}
	if s.Description != nil && (len(*s.Description) > 80 || !description.MatchString(*s.Description)) {
		return nil, fmt.Errorf("the description is 1 to 80 printable ASCII characters but @, with single spaces between words")
	}
	if s.Families.IPv4 == nil && s.Families.IPv6 == nil {
		return nil, fmt.Errorf("it exchanges the routes of no family")
	}
	for _, f := range []struct {
		name string
		fam  *peerFamily
	}{{"ipv4", s.Families.IPv4}, {"ipv6", s.Families.IPv6}} {
		if f.fam == nil {
			continue
		}
		for _, policy := range []*string{f.fam.Import, f.fam.Export} {
			if policy != nil {
				if err := c.policyOfFamily(*policy, f.name); err != nil {
					return nil, fmt.Errorf("families.%s: %w", f.name, err)
				}
			}
		}
		if f.fam.MaxPrefix != nil && *f.fam.MaxPrefix == 0 {
			return nil, fmt.Errorf("families.%s: maxPrefix 0 accepts no route", f.name)
		}
	}
	if err := checkPeerDevices(s, c.vrfs); err != nil {
		return nil, err
	}
	switch {
	case s.Address != nil:
		a, err := neighbourAddress(*s.Address)
		if err != nil {
			return nil, err
		}
		// An IPv6 session carries IPv4 routes with IPv6 next hops alone.
		if a.Is6() && s.Families.IPv4 != nil && !s.ExtendedNextHop {
			return nil, fmt.Errorf("families.ipv4 at the IPv6 address %s needs extendedNextHop, as the routes' next hops are IPv6 addresses", a)
		}
		return []neighbour{{name: a.String()}}, nil
	case s.Interface != nil:
		if !plainInterface.MatchString(*s.Interface) {
			return nil, fmt.Errorf("interface %q is no interface name", *s.Interface)
		}
		if err := linkNeighbour(*s.Interface); err != nil {
			return nil, err
		}
		linkIn, err := c.membership.linkVRF(*s.Interface)
		if err != nil {
			return nil, err
		}
		if linkIn != vrf {
			return nil, fmt.Errorf("interface %s is in the VRF %s, not in the peer's VRF %s", *s.Interface, linkIn, vrf)
		}
		return []neighbour{{name: *s.Interface, onLink: true}}, nil
	}
	if err := checkInterfaces("interfaces", s.Interfaces); err != nil {
		return nil, err
	}
	var ns []neighbour
	// own and other count the links of each glob in the peer's VRF and in other VRFs.
	own, other := map[string]int{}, map[string]int{}
	for _, link := range c.links {
		var globs []string
		for _, glob := range s.Interfaces {
			if matchesGlob(glob, link) {
				globs = append(globs, glob)
			}
		}
		if len(globs) == 0 {
			continue
		}
		linkIn, err := c.membership.linkVRF(link)
		if err != nil {
			return nil, err
		}
		count := own
		if linkIn != vrf {
			count = other
		}
		for _, glob := range globs {
			count[glob]++
		}
		if linkIn != vrf {
			continue
		}
		if err := linkNeighbour(link); err != nil {
			return nil, err
		}
		ns = append(ns, neighbour{name: link, onLink: true})
	}
	for _, glob := range s.Interfaces {
		if own[glob] == 0 && other[glob] > 0 {
			return nil, fmt.Errorf("interfaces: %s matches no link of VRF %s, only links of other VRFs", glob, vrf)
		}
	}
	return ns, nil
}

// namesOneNeighbour reports whether the peer names exactly one of address, interface and
// interfaces.
func (s peerSpec) namesOneNeighbour() bool {
	given := 0
	for _, set := range []bool{s.Address != nil, s.Interface != nil, s.Interfaces != nil} {
		if set {
			given++
		}
	}
	return given == 1
}

// neighbourAddress parses a peer's address, which is a unicast address beyond the router.
func neighbourAddress(s string) (netip.Addr, error) {
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" || a.IsUnspecified() || a.IsMulticast() || a.IsLinkLocalUnicast() || a.IsLoopback() || a.Is4In6() {
		return netip.Addr{}, fmt.Errorf("address %q is no neighbour address: an IPv4 or IPv6 unicast address beyond the router, neither link-local nor IPv4-mapped; a neighbour on a link takes interface or interfaces", s)
	}
	return a, nil
}

// linkNeighbour refuses an interface whose name is an address: bgpd names a session on a link
// after its interface, and a line naming the address would name either.
func linkNeighbour(name string) error {
	if _, err := netip.ParseAddr(name); err == nil {
		return fmt.Errorf("interface %q is an address, which FRR cannot tell from a neighbour's", name)
	}
	return nil
}

// matchesGlob reports whether an interface name or glob, a prefix and *, matches the link.
func matchesGlob(glob, link string) bool {
	if prefix, ok := strings.CutSuffix(glob, "*"); ok {
		return strings.HasPrefix(link, prefix)
	}
	return glob == link
}

// ipWord is the word of FRR's IPv4 or IPv6 commands for a family.
func ipWord(family string) string {
	if family == "ipv4" {
		return "ip"
	}
	return "ipv6"
}

// prefixEntries checks a PrefixList's entries and returns each as frr.conf writes it after the
// sequence number: its action, its masked prefix, and its ge and le.
func prefixEntries(l prefixListSpec) ([]string, error) {
	longest := map[string]int{"ipv4": 32, "ipv6": 128}[l.Family]
	if longest == 0 {
		return nil, fmt.Errorf("family %q is neither ipv4 nor ipv6", l.Family)
	}
	if len(l.Entries) == 0 {
		return nil, fmt.Errorf("it has no entries")
	}
	// seen holds the index of each entry by what it matches.
	seen := map[string]int{}
	var entries []string
	for i, e := range l.Entries {
		p, err := netip.ParsePrefix(e.Prefix)
		if err != nil || familyOf(p.Addr()) != l.Family {
			return nil, fmt.Errorf("%q is no %s address range", e.Prefix, l.Family)
		}
		if e.Action != "permit" && e.Action != "deny" {
			return nil, fmt.Errorf("action %q is neither permit nor deny", e.Action)
		}
		match := p.Masked().String()
		// mgmtd refuses the whole of a config whose ge or le lie outside the prefix's length and
		// the family's longest prefix, or whose ge passes its le.
		if e.GE != nil {
			if *e.GE < p.Bits() || *e.GE > longest || (e.LE != nil && *e.GE > *e.LE) {
				return nil, fmt.Errorf("ge %d of %s lies outside %d to %d, or above le", *e.GE, e.Prefix, p.Bits(), longest)
			}
			match += fmt.Sprintf(" ge %d", *e.GE)
		}
		if e.LE != nil {
			if *e.LE < p.Bits() || *e.LE > longest {
				return nil, fmt.Errorf("le %d of %s lies outside %d to %d", *e.LE, e.Prefix, p.Bits(), longest)
			}
			match += fmt.Sprintf(" le %d", *e.LE)
		}
		// An entry equal to an earlier one never decides, and FRR's prefix-list commands take one
		// for the other.
		if j, ok := seen[match]; ok {
			return nil, fmt.Errorf("entries %d and %d both match %s; the later never decides", j+1, i+1, match)
		}
		seen[match] = i
		entries = append(entries, e.Action+" "+match)
	}
	return entries, nil
}

// prefixLists renders each PrefixList as a prefix-list of its family, an entry a sequence number.
// The numbers follow the entries' positions, so an entry put before others renumbers those after
// it, which frr-reload.py then rewrites.
func (c *frr) prefixLists(b *strings.Builder) {
	for _, l := range c.prefixes {
		for i, e := range c.entries[l.Name] {
			fmt.Fprintf(b, "%s prefix-list %s%s seq %d %s\n", ipWord(l.Spec.Family), prefixListPrefix, l.Name, 5*(i+1), e)
		}
	}
	if len(c.prefixes) > 0 {
		b.WriteString("!\n")
	}
}

// communityList is the community-list line of a community, standard or large, as FRR writes it.
func communityList(name string, seq int, value string) string {
	value, large, _ := community(value)
	kind := "community-list"
	if large {
		kind = "large-community-list"
	}
	return fmt.Sprintf("bgp %s standard %s seq %d permit %s\n", kind, name, seq, value)
}

// communityLists renders each CommunityList, the communities a term removes and a term's AS path
// as FRR's lists.
func (c *frr) communityLists(b *strings.Builder) {
	written := false
	for _, l := range c.communities {
		for i, v := range l.Spec.Communities {
			b.WriteString(communityList(communityPrefix+l.Name, 5*(i+1), v))
			written = true
		}
	}
	for _, p := range c.policies {
		for i, t := range p.Spec.Terms {
			for j, v := range t.Then.RemoveCommunities {
				b.WriteString(communityList(removeList(p.Name, i), 5*(j+1), v))
				written = true
			}
			if t.Match.ASPath != nil {
				fmt.Fprintf(b, "bgp as-path access-list %s seq 5 permit %s\n", asPathList(p.Name, i), *t.Match.ASPath)
				written = true
			}
		}
	}
	if written {
		b.WriteString("!\n")
	}
}

// removeList and asPathList name the lists of term i of a policy.
func removeList(policy string, i int) string { return fmt.Sprintf("rm-%s-%d", policy, termSeq(i)) }
func asPathList(policy string, i int) string { return fmt.Sprintf("asp-%s-%d", policy, termSeq(i)) }

// termSeq is the route-map sequence number of term i; the default is 65535. The numbers follow
// the terms' positions, so a term put before others renumbers those after it, which
// frr-reload.py then rewrites.
func termSeq(i int) int { return 10 * (i + 1) }

// routeMaps renders each RoutingPolicy as a route-map: an entry per term, and its default last,
// and the route-map that accepts every route when a peer's family needs it.
func (c *frr) routeMaps(b *strings.Builder) {
	for _, p := range c.policies {
		for i, t := range p.Spec.Terms {
			action := map[string]string{"accept": "permit", "reject": "deny"}[t.Then.Action]
			fmt.Fprintf(b, "route-map %s%s %s %d\n", policyPrefix, p.Name, action, termSeq(i))
			if l := t.Match.PrefixList; l != nil {
				fmt.Fprintf(b, " match %s address prefix-list %s%s\n", ipWord(c.families[*l]), prefixListPrefix, *l)
			}
			if l := t.Match.CommunityList; l != nil {
				kind := "community"
				if c.large[*l] {
					kind = "large-community"
				}
				fmt.Fprintf(b, " match %s %s%s\n", kind, communityPrefix, *l)
			}
			if t.Match.ASPath != nil {
				fmt.Fprintf(b, " match as-path %s\n", asPathList(p.Name, i))
			}
			th := t.Then
			if th.LocalPref != nil {
				fmt.Fprintf(b, " set local-preference %d\n", *th.LocalPref)
			}
			if th.MED != nil {
				fmt.Fprintf(b, " set metric %d\n", *th.MED)
			}
			var standard, large []string
			for _, v := range th.AddCommunities {
				if v, l, _ := community(v); l {
					large = append(large, v)
				} else {
					standard = append(standard, v)
				}
			}
			if len(standard) > 0 {
				fmt.Fprintf(b, " set community %s additive\n", strings.Join(standard, " "))
			}
			if len(large) > 0 {
				fmt.Fprintf(b, " set large-community %s additive\n", strings.Join(large, " "))
			}
			var removeStandard, removeLarge bool
			for _, v := range th.RemoveCommunities {
				if _, l, _ := community(v); l {
					removeLarge = true
				} else {
					removeStandard = true
				}
			}
			if removeStandard {
				fmt.Fprintf(b, " set comm-list %s delete\n", removeList(p.Name, i))
			}
			if removeLarge {
				fmt.Fprintf(b, " set large-comm-list %s delete\n", removeList(p.Name, i))
			}
			if th.Prepend != nil {
				asns := make([]string, *th.Prepend)
				for k := range asns {
					asns[k] = strconv.FormatUint(uint64(c.prependAS[p.Name]), 10)
				}
				fmt.Fprintf(b, " set as-path prepend %s\n", strings.Join(asns, " "))
			}
			b.WriteString("exit\n")
		}
		fmt.Fprintf(b, "route-map %s%s %s 65535\nexit\n", policyPrefix, p.Name, map[string]string{"accept": "permit", "reject": "deny"}[p.Spec.Default])
	}
	if c.needsAcceptAll() {
		fmt.Fprintf(b, "route-map %s permit 10\nexit\n", acceptAll)
	}
	if len(c.policies) > 0 || c.needsAcceptAll() {
		b.WriteString("!\n")
	}
}

// needsAcceptAll reports whether a peer's family names no import or export policy.
func (c *frr) needsAcceptAll() bool {
	for _, p := range c.peers {
		for _, f := range []*peerFamily{p.Spec.Families.IPv4, p.Spec.Families.IPv6} {
			if f != nil && (f.Import == nil || f.Export == nil) {
				return true
			}
		}
	}
	return false
}

// router renders a BGP instance: its settings, a peer group per peer of its VRF with its sessions,
// and both address families with the networks, each peer's policies and the routes the instance
// imports from other VRFs.
func (c *frr) router(b *strings.Builder, in *instance) {
	g := in.spec
	if in.vrf == defaultVRF {
		fmt.Fprintf(b, "router bgp %d\n", g.ASN)
	} else {
		fmt.Fprintf(b, "router bgp %d vrf %s\n", g.ASN, in.vrf)
	}
	fmt.Fprintf(b, " bgp router-id %s\n no bgp default ipv4-unicast\n", g.RouterID)
	if g.Multipath.ASPathRelax {
		b.WriteString(" bgp bestpath as-path multipath-relax\n")
	}
	if g.GracefulRestart {
		b.WriteString(" bgp graceful-restart\n")
	}
	for _, p := range in.peers {
		group := peerGroupPrefix + p.Name
		s := p.Spec
		fmt.Fprintf(b, " neighbor %s peer-group\n neighbor %s remote-as %s\n", group, group, s.RemoteAS)
		if s.Description != nil {
			fmt.Fprintf(b, " neighbor %s description %s\n", group, *s.Description)
		}
		if s.Password != nil {
			fmt.Fprintf(b, " neighbor %s password @secret_%s.%s@\n", group, s.Password.SecretRef.Name, s.Password.SecretRef.Key)
		}
		if s.BFD {
			fmt.Fprintf(b, " neighbor %s bfd\n neighbor %s bfd profile %s\n", group, group, in.profile())
		}
		if s.ExtendedNextHop {
			fmt.Fprintf(b, " neighbor %s capability extended-nexthop\n", group)
		}
		if s.Shutdown {
			fmt.Fprintf(b, " neighbor %s shutdown\n", group)
		}
		for _, n := range c.neighbours[p.Name] {
			if n.onLink {
				fmt.Fprintf(b, " neighbor %s interface peer-group %s\n", n.name, group)
			} else {
				fmt.Fprintf(b, " neighbor %s peer-group %s\n", n.name, group)
			}
		}
	}
	for _, fam := range []string{"ipv4", "ipv6"} {
		fmt.Fprintf(b, " !\n address-family %s unicast\n", fam)
		for _, n := range g.Networks {
			p, _ := netip.ParsePrefix(n.Prefix)
			if familyOf(p.Addr()) != fam {
				continue
			}
			line := "  network " + p.Masked().String()
			if n.Policy != nil {
				line += " route-map " + policyPrefix + *n.Policy
			}
			b.WriteString(line + "\n")
		}
		fmt.Fprintf(b, "  maximum-paths %d\n  maximum-paths ibgp %d\n", g.Multipath.EBGP, g.Multipath.IBGP)
		for _, p := range in.peers {
			f := p.Spec.Families.IPv4
			if fam == "ipv6" {
				f = p.Spec.Families.IPv6
			}
			if f == nil {
				continue
			}
			group := peerGroupPrefix + p.Name
			fmt.Fprintf(b, "  neighbor %s activate\n", group)
			if f.DefaultOriginate {
				fmt.Fprintf(b, "  neighbor %s default-originate\n", group)
			}
			for _, dir := range []struct {
				policy *string
				word   string
			}{{f.Import, "in"}, {f.Export, "out"}} {
				routeMap := acceptAll
				if dir.policy != nil {
					routeMap = policyPrefix + *dir.policy
				}
				fmt.Fprintf(b, "  neighbor %s route-map %s %s\n", group, routeMap, dir.word)
			}
			if f.MaxPrefix != nil {
				fmt.Fprintf(b, "  neighbor %s maximum-prefix %d\n", group, *f.MaxPrefix)
			}
		}
		if l := in.leak(fam); l != nil {
			if l.Policy != nil {
				fmt.Fprintf(b, "  import vrf route-map %s%s\n", policyPrefix, *l.Policy)
			}
			for _, v := range l.VRFs {
				fmt.Fprintf(b, "  import vrf %s\n", v)
			}
		}
		b.WriteString(" exit-address-family\n")
	}
	b.WriteString("exit\n!\n")
}

// bfd renders a BFD profile of the timers of each instance with a peer of bfd.
func (c *frr) bfd(b *strings.Builder) {
	written := false
	for _, in := range c.instances {
		if !slices.ContainsFunc(in.peers, func(p named[peerSpec]) bool { return p.Spec.BFD }) {
			continue
		}
		if !written {
			b.WriteString("bfd\n")
			written = true
		}
		t := in.spec.BFD
		fmt.Fprintf(b, " profile %s\n  receive-interval %d\n  transmit-interval %d\n  detect-multiplier %d\n exit\n", in.profile(), t.ReceiveMs, t.TransmitMs, t.Multiplier)
	}
	if written {
		b.WriteString("exit\n!\n")
	}
}
