package main

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/router/derive"
)

// frrFile is the file renderFRR writes.
const frrFile = "frr.conf"

// The names frr.conf gives what the documents declare, each kind with a prefix of its own, as
// derive.PeerGroupPrefix starts a BGPPeer's peer group, so a peer group named after a BGPPeer
// never takes the name of an interface or of another list.
const (
	prefixListPrefix = "pl-"
	communityPrefix  = "cl-"
	policyPrefix     = "rp-"
	// acceptAll is the route-map of a peer's family without a policy.
	acceptAll = "accept-all"
	// bfdProfile holds the BFD timers of the default VRF's BGP document; another VRF's profile
	// is bfdProfile-<vrf>.
	bfdProfile = "chalkos"
	// originatePrefix names the route-map do-<list> that sends a peer's default route while a
	// route of the PrefixList exists.
	originatePrefix = "do-"
	// trackPrefix names the route-map track-<vrf>-<family> through which an instance
	// redistributes the routes its peers' defaults track, and leakPrefix the route-map
	// leak-<vrf>-<family> of an import from a VRF that tracks routes of the family.
	trackPrefix = "track-"
	leakPrefix  = "leak-"
	// trackedList is the large-community list that matches trackedCommunity.
	trackedList = "chalkos-tracked"
)

// trackedCommunity marks the routes an instance redistributes to track them, a large community of
// a private-use AS.
var trackedCommunity = derive.Community{Value: "4200000000:0:1", Large: true}

// renderFRR writes FRR's whole config to out/frr.conf: a BGP instance per BGP document, of the
// default VRF or of another, with a peer group per BGPPeer of its VRF, the PrefixList,
// CommunityList and RoutingPolicy documents as prefix-lists, community-lists and route-maps, and a
// BFD profile of each instance's timers. A peer's family with defaultOriginateWhen sends its
// default route while the instance, which redistributes the kernel's routes and the links'
// prefixes that the list matches without announcing them, holds a route of the list. A peer's
// password is a placeholder, @secret_<secret>.<key>@, that frr.service fills in from its
// credentials. A node without a BGP document runs no BGP.
func renderFRR(in map[string]any, out string) error {
	d, err := derive.Build(documents(in))
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

func frrConfig(d derive.Model, node string) (string, error) {
	var b strings.Builder
	b.WriteString("! FRR's config, rendered by chalkos-render frr. frr.service fills in the passwords, and\n" +
		"! frr-reload.py applies the changes.\n" +
		"frr defaults traditional\n")
	if hostName.MatchString(node) {
		b.WriteString("hostname " + node + "\n")
	}
	b.WriteString("log syslog informational\nservice integrated-vtysh-config\n!\n")
	// The BGPPeer's rules refuse a peer whose VRF has no BGP document.
	switch {
	case len(d.BGP) == 0 && len(d.Peers) > 0:
		return "", derive.InternalErrorf("the config holds BGPPeer documents but no BGP document")
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
	peers       []derive.Named[derive.PeerSpec]
	prefixes    []derive.Named[derive.PrefixListSpec]
	communities []derive.Named[derive.CommunityListSpec]
	policies    []derive.Named[derive.RoutingPolicySpec]
	// families are the PrefixList documents' families, and large whether a CommunityList holds
	// large communities, by name.
	families map[string]derive.Family
	large    map[string]bool
	// entries are each PrefixList's entries as frr.conf writes them, by name.
	entries map[string][]string
	// policyNames holds the RoutingPolicy documents by name, which MustGet finds a reference in.
	policyNames map[string]derive.RoutingPolicySpec
	// neighbours are the sessions of each peer, by peer name.
	neighbours map[string][]derive.Session
	// prependAS is the AS number the prepends of each RoutingPolicy with one repeat, by name.
	prependAS map[string]uint32
	// tracked are the PrefixLists the peers' defaults track, by VRF and family, sorted.
	tracked map[string]map[derive.Family][]string
}

// An instance is a BGP document: the BGP instance of one VRF, and the peers in that VRF.
type instance struct {
	name, vrf string
	spec      derive.BGPSpec
	peers     []derive.Named[derive.PeerSpec]
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
func (in *instance) leak(family derive.Family) *derive.LeakSpec {
	if family == derive.IPv4 {
		return in.spec.Import.IPv4
	}
	return in.spec.Import.IPv6
}

// profile is the BFD profile of the instance's timers: bfdProfile for the default VRF's, and
// bfdProfile-<vrf> for another's.
func (in *instance) profile() string {
	if in.vrf == derive.DefaultVRF {
		return bfdProfile
	}
	return bfdProfile + "-" + in.vrf
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
// dots, underscores and dashes alone. frr.conf writes the name into the password's placeholder,
// which frr.service's helper reads with a pattern of these characters.
var credential = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// description is a BGPPeer's description as the schema takes it: printable ASCII without the
// spaces at its ends or in a row that FRR's parser would drop, and without an @, so that it never
// holds a password placeholder. frr.conf writes it to the end of its line, which a line break
// would end.
var description = regexp.MustCompile(`^[!-?A-~]+( [!-?A-~]+)*$`)

// newFRR checks what FRR's config alone sees, takes each peer's sessions from the model, which
// Model.Sessions expands against the links of its VRF, and notes the PrefixLists each VRF's peers
// track. It trusts what chalkd checked before it renders, and guards with an internal error what
// frr.conf writes unquoted, which a value could add a command to, and what would panic.
func newFRR(d derive.Model) (*frr, error) {
	c := &frr{
		peers:       d.Peers,
		prefixes:    d.Prefixes,
		communities: d.Communities,
		policies:    d.RoutingPolicies,
		families:    map[string]derive.Family{},
		large:       map[string]bool{},
		entries:     map[string][]string{},
		policyNames: derive.ByName(d.RoutingPolicies),
		neighbours:  map[string][]derive.Session{},
		prependAS:   map[string]uint32{},
		tracked:     map[string]map[derive.Family][]string{},
	}
	for _, g := range d.BGP {
		in, err := newInstance(g, d.VRFs)
		if err != nil {
			return nil, err
		}
		c.instances = append(c.instances, in)
	}
	// FRR takes the default VRF's instance first.
	slices.SortStableFunc(c.instances, func(a, b *instance) int { return derive.CompareVRFs(a.vrf, b.vrf) })
	for _, l := range d.Prefixes {
		c.families[l.Name] = l.Spec.Family
		c.entries[l.Name] = prefixEntries(l.Spec)
	}
	// The CommunityList's rules make a list's communities all standard or all large ones, so the
	// first tells which.
	for _, l := range d.Communities {
		c.large[l.Name] = len(l.Spec.Communities) > 0 && l.Spec.Communities[0].Large
	}
	for _, p := range d.RoutingPolicies {
		if err := c.checkPolicy(p); err != nil {
			return nil, derive.Within("RoutingPolicy "+p.Name, err)
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
	// The sessions of each peer, which the validator router has checked, and who names each
	// neighbour in any VRF, so none takes a peer group's name.
	peerSessions, err := d.Sessions()
	if err != nil {
		return nil, err
	}
	sessions := map[string]string{}
	for _, p := range d.Peers {
		vrf := derive.VRFOf(p.Spec.VRF)
		// The BGPPeer's rules refuse a peer of a VRF without a BGP document; without its
		// instance the peer would have no block to sit in.
		in := c.served(vrf)
		if in == nil {
			return nil, derive.InternalErrorf("BGPPeer %s runs in the VRF %s, which no BGP document serves", p.Name, vrf)
		}
		if err := c.checkPeer(p); err != nil {
			return nil, derive.Within("BGPPeer "+p.Name, err)
		}
		for _, s := range peerSessions[p.Name] {
			sessions[s.Neighbour] = p.Name
		}
		c.neighbours[p.Name] = peerSessions[p.Name]
		in.peers = append(in.peers, p)
		for fam, f := range map[derive.Family]*derive.PeerFamily{derive.IPv4: p.Spec.Families.IPv4, derive.IPv6: p.Spec.Families.IPv6} {
			if f != nil && f.DefaultOriginateWhen != nil {
				c.track(vrf, fam, *f.DefaultOriginateWhen)
			}
		}
	}
	for _, p := range d.Peers {
		if peer, ok := sessions[derive.PeerGroupPrefix+p.Name]; ok {
			return nil, refusals.Errorf("BGPPeer", p.Name, "metadata.name", "BGPPeer %s names %s%s, the peer group of BGPPeer %s; rename the document or the interface", peer, derive.PeerGroupPrefix, p.Name, p.Name)
		}
	}
	if err := c.prepends(); err != nil {
		return nil, err
	}
	return c, nil
}

// newInstance takes a BGP document. The schemas and the BGP kind's rules check its AS number, its
// router ID, counts and timers; frr.conf writes the VRF's name and the router ID unquoted, so it
// guards both: the VRF is a VRF document's, whose name Build checked, unless it is the
// default one, and the router ID is an IPv4 address, which FRR takes.
func newInstance(g derive.Named[derive.BGPSpec], vrfs []derive.Named[derive.VRFSpec]) (*instance, error) {
	b := g.Spec
	in := &instance{name: g.Name, vrf: derive.VRFOf(b.VRF), spec: b}
	what := "BGP " + g.Name
	if in.vrf != derive.DefaultVRF {
		if _, err := derive.MustGet(derive.ByName(vrfs), "VRF", in.vrf); err != nil {
			return nil, derive.Within(what, err)
		}
	}
	if !b.RouterID.Is4() {
		return nil, derive.InternalErrorf("%s: routerID %s is no IPv4 address", what, b.RouterID)
	}
	return in, nil
}

// checkNetworks guards an instance's networks: a policy that names no RoutingPolicy, whose name
// frr.conf writes unquoted. The BGP kind's rules refuse a default route among the networks of a
// VRF other than the default one and a policy of another family.
func (c *frr) checkNetworks(in *instance) error {
	for _, n := range in.spec.Networks {
		if n.Policy != nil {
			if err := c.policy(*n.Policy); err != nil {
				return derive.Within(fmt.Sprintf("BGP %s: network %s", in.name, n.Prefix), err)
			}
		}
	}
	return nil
}

// checkImports guards an instance's imports, whose VRFs and policy frr.conf writes unquoted: each
// VRF is an instance's, whose name the documents' checks took, and the policy a RoutingPolicy's.
// The BGP kind's rules refuse an import of its own VRF or of a VRF without a BGP document, and a
// policy of another family; the VRF kind's rules an import into or out of a VRF with a conntrack
// zone.
func (c *frr) checkImports(in *instance) error {
	for _, f := range []struct {
		family string
		leak   *derive.LeakSpec
	}{{"ipv4", in.spec.Import.IPv4}, {"ipv6", in.spec.Import.IPv6}} {
		if f.leak == nil {
			continue
		}
		what := fmt.Sprintf("BGP %s: import.%s", in.name, f.family)
		for _, v := range f.leak.VRFs {
			if c.served(v) == nil {
				return derive.InternalErrorf("%s names the VRF %q, which no BGP document serves", what, v)
			}
		}
		if f.leak.Policy != nil {
			if err := c.policy(*f.leak.Policy); err != nil {
				return derive.Within(what, err)
			}
		}
	}
	return nil
}

// track notes a PrefixList that a family of a peer in the VRF tracks, once.
func (c *frr) track(vrf string, family derive.Family, list string) {
	if c.tracked[vrf] == nil {
		c.tracked[vrf] = map[derive.Family][]string{}
	}
	if !slices.Contains(c.tracked[vrf][family], list) {
		c.tracked[vrf][family] = append(c.tracked[vrf][family], list)
		slices.Sort(c.tracked[vrf][family])
	}
}

// leaksTracked reports whether the instance imports routes of the family from a VRF whose
// instance tracks routes of that family, which the import keeps out: the kernel would install
// them in the importing VRF's table.
func (c *frr) leaksTracked(in *instance, family derive.Family) bool {
	l := in.leak(family)
	return l != nil && slices.ContainsFunc(l.VRFs, func(v string) bool { return len(c.tracked[v][family]) > 0 })
}

// trackMap and leakMap name an instance's route-maps of a family.
func trackMap(vrf string, family derive.Family) string {
	return trackPrefix + vrf + "-" + string(family)
}
func leakMap(vrf string, family derive.Family) string {
	return leakPrefix + vrf + "-" + string(family)
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
		for _, fam := range []derive.Family{derive.IPv4, derive.IPv6} {
			if l := in.leak(fam); l != nil {
				use(l.Policy, asn)
			}
		}
		for _, p := range in.peers {
			for _, f := range []*derive.PeerFamily{p.Spec.Families.IPv4, p.Spec.Families.IPv6} {
				if f != nil {
					use(f.Import, asn)
					use(f.Export, asn)
				}
			}
		}
	}
	for _, p := range c.policies {
		// first is the first term that prepends, which a refusal names.
		first := -1
		for i, t := range p.Spec.Terms {
			if first < 0 && t.Then.Prepend != nil {
				first = i
			}
		}
		if first < 0 {
			continue
		}
		switch asns := slices.Sorted(maps.Keys(uses[p.Name])); len(asns) {
		case 0:
			c.prependAS[p.Name] = c.instances[0].spec.ASN
		case 1:
			c.prependAS[p.Name] = asns[0]
		default:
			return refusals.Errorf("RoutingPolicy", p.Name, fmt.Sprintf("spec.terms[%d].then.prepend", first), "RoutingPolicy %s prepends the AS number, and BGP instances of AS %d and %d use it; give each AS a policy of its own", p.Name, asns[0], asns[1])
		}
	}
	return nil
}

// checkPolicy checks a RoutingPolicy's AS paths as bgpd compiles them, which no rule can, and
// guards what frr.conf writes unquoted: the lists a term names and its AS path. It guards the
// prepend's count too, which sizes a list. The RoutingPolicy's rules check the number of terms,
// the PrefixLists' family, a rejecting term that sets nothing and an AS path's length; the
// schema checks the actions and the communities, and derive decodes no others.
func (c *frr) checkPolicy(p derive.Named[derive.RoutingPolicySpec]) error {
	for i, t := range p.Spec.Terms {
		term := fmt.Sprintf("term %d", i+1)
		if l := t.Match.PrefixList; l != nil {
			if _, err := derive.MustGet(c.families, "PrefixList", *l); err != nil {
				return derive.Within(term, err)
			}
		}
		if l := t.Match.CommunityList; l != nil {
			if _, err := derive.MustGet(c.large, "CommunityList", *l); err != nil {
				return derive.Within(term, err)
			}
		}
		if r := t.Match.ASPath; r != nil {
			if !asPathRegex.MatchString(*r) {
				return derive.InternalErrorf("%s: asPath %q holds more than digits, single spaces and _^$.*+?()|{},-", term, *r)
			}
			if err := checkASPath(*r); err != nil {
				return refusals.Errorf("RoutingPolicy", p.Name, fmt.Sprintf("spec.terms[%d].match.asPath", i), "asPath %q is no regular expression bgpd compiles: %v", *r, err)
			}
		}
		th := t.Then
		// The count sizes the list of AS numbers routeMaps writes: one below 0 would panic, and a
		// large one would take the memory.
		if th.Prepend != nil && (*th.Prepend < 1 || *th.Prepend > 10) {
			return derive.InternalErrorf("%s: prepend %d lies outside 1 to 10", term, *th.Prepend)
		}
	}
	return nil
}

// policy guards a reference to a RoutingPolicy, whose name frr.conf writes unquoted in the
// route-map's: it names a RoutingPolicy document, whose name Build checked.
func (c *frr) policy(name string) error {
	_, err := derive.MustGet(c.policyNames, "RoutingPolicy", name)
	return err
}

// checkPeer guards what frr.conf writes of a peer unquoted, its password's Secret and key and its
// description, and the RoutingPolicy and PrefixList documents its families name. The BGPPeer's
// schema and rules check the rest of the peer, and derive.Model.Sessions its sessions.
func (c *frr) checkPeer(p derive.Named[derive.PeerSpec]) error {
	s := p.Spec
	if s.Password != nil && (!credential.MatchString(s.Password.SecretRef.Name) || !credential.MatchString(s.Password.SecretRef.Key)) {
		return derive.InternalErrorf("the password's Secret %q and key %q are letters, digits, '.', '_' and '-' alone", s.Password.SecretRef.Name, s.Password.SecretRef.Key)
	}
	if s.Description != nil && !description.MatchString(*s.Description) {
		return derive.InternalErrorf("the description is printable ASCII characters but @, with single spaces between words")
	}
	for _, f := range []struct {
		name string
		fam  *derive.PeerFamily
	}{{"ipv4", s.Families.IPv4}, {"ipv6", s.Families.IPv6}} {
		if f.fam == nil {
			continue
		}
		for _, policy := range []*string{f.fam.Import, f.fam.Export} {
			if policy != nil {
				if err := c.policy(*policy); err != nil {
					return derive.Within("families."+f.name, err)
				}
			}
		}
		if l := f.fam.DefaultOriginateWhen; l != nil {
			if _, err := derive.MustGet(c.families, "PrefixList", *l); err != nil {
				return derive.Within(fmt.Sprintf("families.%s: defaultOriginateWhen", f.name), err)
			}
		}
	}
	return nil
}

// routeMapAction is the action of a route-map entry that takes or refuses a route.
var routeMapAction = map[derive.RouteAction]string{"accept": "permit", "reject": "deny"}

// ipWord is the word of FRR's IPv4 or IPv6 commands for a family.
func ipWord(family derive.Family) string {
	if family == derive.IPv4 {
		return "ip"
	}
	return "ipv6"
}

// prefixEntries returns each of a PrefixList's entries as frr.conf writes it after the sequence
// number: its action, its masked prefix, and its ge and le. The uniqueness check has refused two
// entries of one prefix, ge and le, which FRR's prefix-list commands would take one for the
// other, and the PrefixList's rules entries of another family and a ge or le outside the
// prefix's length and the family's longest prefix, which mgmtd would refuse with every list and
// route-map of the config.
func prefixEntries(l derive.PrefixListSpec) []string {
	var entries []string
	for _, e := range l.Entries {
		match := e.Prefix.Masked().String()
		if e.GE != nil {
			match += fmt.Sprintf(" ge %d", *e.GE)
		}
		if e.LE != nil {
			match += fmt.Sprintf(" le %d", *e.LE)
		}
		entries = append(entries, string(e.Action)+" "+match)
	}
	return entries
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
func communityList(name string, seq int, value derive.Community) string {
	kind := "community-list"
	if value.Large {
		kind = "large-community-list"
	}
	return fmt.Sprintf("bgp %s standard %s seq %d permit %s\n", kind, name, seq, value)
}

// communityLists renders each CommunityList, the communities a term removes, a term's AS path and,
// for the imports that keep tracked routes out, the tracked community, as FRR's lists.
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
	if slices.ContainsFunc(c.instances, func(in *instance) bool { return c.leaksTracked(in, "ipv4") || c.leaksTracked(in, "ipv6") }) {
		b.WriteString(communityList(trackedList, 5, trackedCommunity))
		written = true
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
// the route-map that accepts every route when a peer's family needs it, and those of the defaults
// sent while a route exists.
func (c *frr) routeMaps(b *strings.Builder) {
	for _, p := range c.policies {
		for i, t := range p.Spec.Terms {
			action := routeMapAction[t.Then.Action]
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
				if v.Large {
					large = append(large, v.Value)
				} else {
					standard = append(standard, v.Value)
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
				if v.Large {
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
		fmt.Fprintf(b, "route-map %s%s %s 65535\nexit\n", policyPrefix, p.Name, routeMapAction[p.Spec.Default])
	}
	if c.needsAcceptAll() {
		fmt.Fprintf(b, "route-map %s permit 10\nexit\n", acceptAll)
	}
	tracking := c.trackingMaps(b)
	if len(c.policies) > 0 || c.needsAcceptAll() || tracking {
		b.WriteString("!\n")
	}
}

// trackingMaps renders the route-maps of the defaults sent while a route exists: do-<list> for
// each PrefixList a peer tracks, and per instance and family track-<vrf>-<family>, through which
// the instance redistributes the routes its peers track, and leak-<vrf>-<family> for an import
// from a VRF that tracks routes of the family. Blackhole routes, the VRF tables' unreachable
// defaults among them, count for nothing; zebra reads their kernel metric as a distance and a
// metric of its own, so the route-map matches their type. A tracked path takes distance 255,
// so a path a peer sends for the same prefix stays the best one, which bgpd installs, announces
// and leaks. It takes weight 0 too: bgpd compares no distance against a network of the
// instance, and the network keeps its weight of 32768, so it stays the best path even when its
// policy prepends or lowers its local preference. It reports whether it rendered any.
func (c *frr) trackingMaps(b *strings.Builder) bool {
	lists := map[string]bool{}
	for _, byFamily := range c.tracked {
		for _, ls := range byFamily {
			for _, l := range ls {
				lists[l] = true
			}
		}
	}
	for _, l := range slices.Sorted(maps.Keys(lists)) {
		fmt.Fprintf(b, "route-map %s%s permit 10\n match %s address prefix-list %s%s\nexit\n", originatePrefix, l, ipWord(c.families[l]), prefixListPrefix, l)
	}
	written := len(lists) > 0
	for _, in := range c.instances {
		for _, fam := range []derive.Family{derive.IPv4, derive.IPv6} {
			if ls := c.tracked[in.vrf][fam]; len(ls) > 0 {
				name := trackMap(in.vrf, fam)
				fmt.Fprintf(b, "route-map %s deny 5\n match %s next-hop type blackhole\nexit\n", name, ipWord(fam))
				for i, l := range ls {
					fmt.Fprintf(b, "route-map %s permit %d\n match %s address prefix-list %s%s\n set community no-advertise additive\n set distance 255\n set large-community %s additive\n set weight 0\nexit\n",
						name, 10*(i+1), ipWord(fam), prefixListPrefix, l, trackedCommunity)
				}
			}
			if c.leaksTracked(in, fam) {
				name := leakMap(in.vrf, fam)
				fmt.Fprintf(b, "route-map %s deny 5\n match large-community %s\nexit\nroute-map %s permit 10\n", name, trackedList, name)
				if p := in.leak(fam).Policy; p != nil {
					fmt.Fprintf(b, " call %s%s\n", policyPrefix, *p)
				}
				b.WriteString("exit\n")
				written = true
			}
		}
	}
	return written
}

// needsAcceptAll reports whether a peer's family names no import or export policy.
func (c *frr) needsAcceptAll() bool {
	for _, p := range c.peers {
		for _, f := range []*derive.PeerFamily{p.Spec.Families.IPv4, p.Spec.Families.IPv6} {
			if f != nil && (f.Import == nil || f.Export == nil) {
				return true
			}
		}
	}
	return false
}

// router renders a BGP instance: its settings, a peer group per peer of its VRF with its sessions
// or its listen range, and both address families with the networks, the redistribution of the
// routes its peers' defaults track, each peer's policies and the routes the instance imports from
// other VRFs.
func (c *frr) router(b *strings.Builder, in *instance) {
	g := in.spec
	if in.vrf == derive.DefaultVRF {
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
		group := derive.PeerGroupPrefix + p.Name
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
		if s.Listen != nil {
			// In a VRF's instance the line opens the VRF's own listening socket, and bgpd sets the
			// group's password on it for the range, as it does on the default VRF's socket.
			fmt.Fprintf(b, " bgp listen range %s peer-group %s\n", s.Listen.Range.Masked(), group)
		}
		for _, n := range c.neighbours[p.Name] {
			if n.OnLink {
				fmt.Fprintf(b, " neighbor %s interface peer-group %s\n", n.Neighbour, group)
			} else {
				fmt.Fprintf(b, " neighbor %s peer-group %s\n", n.Neighbour, group)
			}
		}
	}
	for _, fam := range []derive.Family{derive.IPv4, derive.IPv6} {
		fmt.Fprintf(b, " !\n address-family %s unicast\n", fam)
		for _, n := range g.Networks {
			p := n.Prefix
			if derive.FamilyOf(p.Addr()) != fam {
				continue
			}
			line := "  network " + p.Masked().String()
			if n.Policy != nil {
				line += " route-map " + policyPrefix + *n.Policy
			}
			b.WriteString(line + "\n")
		}
		if len(c.tracked[in.vrf][fam]) > 0 {
			m := trackMap(in.vrf, fam)
			fmt.Fprintf(b, "  redistribute kernel route-map %s\n  redistribute connected route-map %s\n", m, m)
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
			group := derive.PeerGroupPrefix + p.Name
			fmt.Fprintf(b, "  neighbor %s activate\n", group)
			switch {
			case f.DefaultOriginate && f.DefaultOriginateWhen != nil:
				fmt.Fprintf(b, "  neighbor %s default-originate route-map %s%s\n", group, originatePrefix, *f.DefaultOriginateWhen)
			case f.DefaultOriginate:
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
			switch {
			case c.leaksTracked(in, fam):
				fmt.Fprintf(b, "  import vrf route-map %s\n", leakMap(in.vrf, fam))
			case l.Policy != nil:
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
		if !slices.ContainsFunc(in.peers, func(p derive.Named[derive.PeerSpec]) bool { return p.Spec.BFD }) {
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
