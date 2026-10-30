package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The documents of the router layer's kinds, as the outputs router-sysctl and router-nftables read
// them: with the defaults of their schemas, each kind's sorted by name.

type routerSpec struct {
	Forwarding struct {
		IPv4 bool `json:"ipv4"`
		IPv6 bool `json:"ipv6"`
	} `json:"forwarding"`
	RPFilter     string `json:"rpFilter"`
	ECMPHash     string `json:"ecmpHash"`
	ConntrackMax *int64 `json:"conntrackMax"`
	Management   struct {
		// Interfaces is nil for every interface.
		Interfaces []string `json:"interfaces"`
	} `json:"management"`
}

// defaultRouter is the Router of a node whose config holds none: the kind's defaults.
func defaultRouter() routerSpec {
	var r routerSpec
	r.Forwarding.IPv4, r.Forwarding.IPv6 = true, true
	r.RPFilter, r.ECMPHash = "loose", "l4"
	return r
}

type zoneSpec struct {
	Interfaces []string `json:"interfaces"`
	// VRFs are the VRF documents whose links are all in the zone.
	VRFs []string `json:"vrfs"`
}

type policySpec struct {
	From    string         `json:"from"`
	To      string         `json:"to"`
	Default string         `json:"default"`
	Rules   []firewallRule `json:"rules"`
}

type firewallRule struct {
	Family           *string     `json:"family"`
	Protocol         *string     `json:"protocol"`
	SourcePorts      []portRange `json:"sourcePorts"`
	DestinationPorts []portRange `json:"destinationPorts"`
	Source           []string    `json:"source"`
	Destination      []string    `json:"destination"`
	ICMPTypes        []string    `json:"icmpTypes"`
	Action           string      `json:"action"`
	Log              bool        `json:"log"`
}

type addressSetSpec struct {
	Family   string   `json:"family"`
	Prefixes []string `json:"prefixes"`
}

type natSpec struct {
	Type   string    `json:"type"`
	Family string    `json:"family"`
	Out    *natPlace `json:"out"`
	In     *natPlace `json:"in"`
	Match  struct {
		Source           []string    `json:"source"`
		Destination      []string    `json:"destination"`
		Protocol         *string     `json:"protocol"`
		DestinationPorts []portRange `json:"destinationPorts"`
	} `json:"match"`
	To *struct {
		Address string `json:"address"`
		Port    *int   `json:"port"`
	} `json:"to"`
}

type natPlace struct {
	Zone      *string `json:"zone"`
	Interface *string `json:"interface"`
}

// portRange is a port from 1, or a range of ports "<first>-<last>", as nftables writes both.
type portRange string

func (p *portRange) UnmarshalJSON(data []byte) error {
	var n uint16
	if err := json.Unmarshal(data, &n); err == nil && n > 0 {
		*p = portRange(strconv.Itoa(int(n)))
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("a port is a number from 1 to 65535 or a range \"<first>-<last>\", not %s", data)
	}
	first, last, ok := strings.Cut(s, "-")
	a, errA := strconv.ParseUint(first, 10, 16)
	b, errB := strconv.ParseUint(last, 10, 16)
	if !ok || errA != nil || errB != nil || a == 0 || a > b {
		return fmt.Errorf("%q is no range of ports \"<first>-<last>\" from 1 to 65535 that ends no lower than it starts", s)
	}
	*p = portRange(fmt.Sprintf("%d-%d", a, b))
	return nil
}

type bgpSpec struct {
	// VRF is nil for the default VRF's instance.
	VRF       *string `json:"vrf"`
	ASN       uint32  `json:"asn"`
	RouterID  string  `json:"routerID"`
	Multipath struct {
		EBGP        int  `json:"ebgp"`
		IBGP        int  `json:"ibgp"`
		ASPathRelax bool `json:"asPathRelax"`
	} `json:"multipath"`
	BFD struct {
		ReceiveMs  int `json:"receiveMs"`
		TransmitMs int `json:"transmitMs"`
		Multiplier int `json:"multiplier"`
	} `json:"bfd"`
	GracefulRestart bool `json:"gracefulRestart"`
	Networks        []struct {
		Prefix string  `json:"prefix"`
		Policy *string `json:"policy"`
	} `json:"networks"`
	Import struct {
		IPv4 *leakSpec `json:"ipv4"`
		IPv6 *leakSpec `json:"ipv6"`
	} `json:"import"`
}

// leakSpec is what a BGP instance imports of one family from other VRFs' instances.
type leakSpec struct {
	VRFs   []string `json:"vrfs"`
	Policy *string  `json:"policy"`
}

type peerSpec struct {
	// VRF is nil for a peer of the default VRF.
	VRF       *string `json:"vrf"`
	Address   *string `json:"address"`
	Interface *string `json:"interface"`
	// Interfaces is nil when the peer names an address or an interface.
	Interfaces []string `json:"interfaces"`
	RemoteAS   peerAS   `json:"remoteAS"`
	Password   *struct {
		SecretRef struct {
			Name string `json:"name"`
			Key  string `json:"key"`
		} `json:"secretRef"`
	} `json:"password"`
	BFD             bool `json:"bfd"`
	ExtendedNextHop bool `json:"extendedNextHop"`
	Families        struct {
		IPv4 *peerFamily `json:"ipv4"`
		IPv6 *peerFamily `json:"ipv6"`
	} `json:"families"`
	Description *string `json:"description"`
	Shutdown    bool    `json:"shutdown"`
}

type peerFamily struct {
	Import           *string `json:"import"`
	Export           *string `json:"export"`
	DefaultOriginate bool    `json:"defaultOriginate"`
	MaxPrefix        *uint32 `json:"maxPrefix"`
}

// peerAS is a BGPPeer's remoteAS as FRR's remote-as takes it: external, internal or a number.
type peerAS string

func (a *peerAS) UnmarshalJSON(data []byte) error {
	var n uint32
	if err := json.Unmarshal(data, &n); err == nil && n > 0 {
		*a = peerAS(strconv.FormatUint(uint64(n), 10))
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil || (s != "external" && s != "internal") {
		return fmt.Errorf("remoteAS is external, internal or an AS number from 1 to 4294967295, not %s", data)
	}
	*a = peerAS(s)
	return nil
}

type prefixListSpec struct {
	Family  string `json:"family"`
	Entries []struct {
		Prefix string `json:"prefix"`
		GE     *int   `json:"ge"`
		LE     *int   `json:"le"`
		Action string `json:"action"`
	} `json:"entries"`
}

type communityListSpec struct {
	Communities []string `json:"communities"`
}

type routingPolicySpec struct {
	Terms []struct {
		Match struct {
			PrefixList    *string `json:"prefixList"`
			CommunityList *string `json:"communityList"`
			ASPath        *string `json:"asPath"`
		} `json:"match"`
		Then struct {
			Action            string   `json:"action"`
			LocalPref         *uint32  `json:"localPref"`
			MED               *uint32  `json:"med"`
			AddCommunities    []string `json:"addCommunities"`
			RemoveCommunities []string `json:"removeCommunities"`
			Prepend           *int     `json:"prepend"`
		} `json:"then"`
	} `json:"terms"`
	Default string `json:"default"`
}

type nat64Spec struct {
	// VRF is nil for a NAT64 of the default VRF.
	VRF    *string     `json:"vrf"`
	Prefix string      `json:"prefix"`
	Pool4  []poolEntry `json:"pool4"`
}

type poolEntry struct {
	Prefix    string    `json:"prefix"`
	Ports     portRange `json:"ports"`
	Protocols []string  `json:"protocols"`
}

type resolverSpec struct {
	// VRF is nil for the default VRF's resolver.
	VRF        *string  `json:"vrf"`
	Listen     []string `json:"listen"`
	Allow      []string `json:"allow"`
	Forwarders []string `json:"forwarders"`
	DNSSEC     bool     `json:"dnssec"`
	DNS64      *struct {
		Prefix string `json:"prefix"`
	} `json:"dns64"`
	LocalZones []localZone `json:"localZones"`
}

type localZone struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Records []struct {
		Name  string `json:"name"`
		Type  string `json:"type"`
		Value string `json:"value"`
		TTL   uint32 `json:"ttl"`
	} `json:"records"`
}

type vrfSpec struct {
	Table      uint32   `json:"table"`
	Interfaces []string `json:"interfaces"`
	// ConntrackZone is nil for a VRF that shares zone 0 with the default VRF.
	ConntrackZone *int `json:"conntrackZone"`
}

// named is the spec of a document, with the document's name.
type named[T any] struct {
	Name string
	Spec T
}

// routerDocuments are the router kinds' documents of an output's input.
type routerDocuments struct {
	Routers  []named[routerSpec]
	Zones    []named[zoneSpec]
	Policies []named[policySpec]
	Sets     []named[addressSetSpec]
	NAT      []named[natSpec]
	BGP      []named[bgpSpec]
	Peers    []named[peerSpec]
	Prefixes []named[prefixListSpec]
	// Communities are the CommunityList documents and RoutingPolicies the RoutingPolicy ones.
	Communities     []named[communityListSpec]
	RoutingPolicies []named[routingPolicySpec]
	NAT64           []named[nat64Spec]
	Resolvers       []named[resolverSpec]
	VRFs            []named[vrfSpec]
	// Links are the interface names the Network documents' [Match] sections name, sorted.
	Links []string
	// Addresses are the static addresses the Network documents give their links.
	Addresses []networkAddress
	// Membership is the VRF of the links of Links and of the Network documents.
	Membership vrfMembership
}

// readRouterDocuments decodes the router kinds' documents of in; a kind the output does not read
// has none.
func readRouterDocuments(in map[string]any) (routerDocuments, error) {
	byKind, _ := in["documents"].(map[string]any)
	var d routerDocuments
	var err error
	if d.Routers, err = decodeKind[routerSpec](byKind, "Router"); err != nil {
		return d, err
	}
	if d.Zones, err = decodeKind[zoneSpec](byKind, "FirewallZone"); err != nil {
		return d, err
	}
	if d.Policies, err = decodeKind[policySpec](byKind, "FirewallPolicy"); err != nil {
		return d, err
	}
	if d.Sets, err = decodeKind[addressSetSpec](byKind, "AddressSet"); err != nil {
		return d, err
	}
	if d.NAT, err = decodeKind[natSpec](byKind, "NATRule"); err != nil {
		return d, err
	}
	if d.BGP, err = decodeKind[bgpSpec](byKind, "BGP"); err != nil {
		return d, err
	}
	if d.Peers, err = decodeKind[peerSpec](byKind, "BGPPeer"); err != nil {
		return d, err
	}
	if d.Prefixes, err = decodeKind[prefixListSpec](byKind, "PrefixList"); err != nil {
		return d, err
	}
	if d.Communities, err = decodeKind[communityListSpec](byKind, "CommunityList"); err != nil {
		return d, err
	}
	if d.RoutingPolicies, err = decodeKind[routingPolicySpec](byKind, "RoutingPolicy"); err != nil {
		return d, err
	}
	if d.NAT64, err = decodeKind[nat64Spec](byKind, "NAT64"); err != nil {
		return d, err
	}
	if d.Resolvers, err = decodeKind[resolverSpec](byKind, "Resolver"); err != nil {
		return d, err
	}
	if d.VRFs, err = decodeKind[vrfSpec](byKind, "VRF"); err != nil {
		return d, err
	}
	d.Links = networkLinks(byKind)
	d.Addresses = networkAddresses(byKind)
	if d.Membership, err = vrfLinks(d.VRFs, d.Links, byKind); err != nil {
		return d, err
	}
	return d, nil
}

// networkLinks are the interface names in the [Match] sections' Name= of the Network documents
// of byKind, sorted and once each, as matchLinks reads them.
func networkLinks(byKind map[string]any) []string {
	docs, _ := byKind["Network"].([]any)
	seen := map[string]bool{}
	for _, d := range docs {
		doc, _ := d.(map[string]any)
		spec, _ := doc["spec"].(map[string]any)
		for _, l := range matchLinks(spec) {
			seen[l] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// networkAddress is a static address that the Network document network gives its links.
type networkAddress struct {
	network string
	prefix  netip.Prefix
}

// networkAddresses are the addresses of Address= in the [Network] and [Address] sections and of
// ServerAddress= in the [DHCPServer] section of the Network documents of byKind that parse as an
// address, with or without a prefix length, once the spaces around them go; others, such as an
// empty value, give none.
func networkAddresses(byKind map[string]any) []networkAddress {
	var out []networkAddress
	docs, _ := byKind["Network"].([]any)
	for _, d := range docs {
		doc, _ := d.(map[string]any)
		name, err := documentName("Network", doc)
		if err != nil {
			continue
		}
		spec, _ := doc["spec"].(map[string]any)
		for _, key := range []struct{ section, key string }{{"network", "address"}, {"address", "address"}, {"dhcpServer", "serverAddress"}} {
			for _, s := range oneOrMany(spec[key.section]) {
				section, _ := s.(map[string]any)
				for _, v := range oneOrMany(section[key.key]) {
					text, _ := v.(string)
					text = strings.TrimSpace(text)
					p, err := netip.ParsePrefix(text)
					if err != nil {
						a, err := netip.ParseAddr(text)
						if err != nil {
							continue
						}
						p = netip.PrefixFrom(a, a.BitLen())
					}
					out = append(out, networkAddress{network: name, prefix: p})
				}
			}
		}
	}
	return out
}

// networkVRFOf is the VRF of the links of the Network document named n.
func (d routerDocuments) networkVRFOf(n string) string {
	if vrf, ok := d.Membership.Networks[n]; ok {
		return vrf
	}
	return defaultVRF
}

// matchLinks are the interface names in the [Match] sections' Name= of a Network document's
// spec, as parseNameMatch reads it: the words that are neither globs nor negated. A Name= that
// parseNameMatch refuses names the links of the words before the one it refuses.
func matchLinks(spec map[string]any) []string {
	m, _ := parseNameMatch("", spec)
	var links []string
	for _, w := range m.words {
		if !w.negated && isLiteral(w.glob) && interfaceName.MatchString(w.glob) {
			links = append(links, w.glob)
		}
	}
	return links
}

// nameMatch is the Name= of a Network document's [Match] sections as systemd-networkd reads it.
// It matches a link that no negated word matches and, when there is a word that is not negated,
// that one of those matches; without words it matches every link.
type nameMatch struct {
	words []nameWord
}

// nameWord is a word of Name=: glob is the word as path.Match takes it, text as Name= gives it.
type nameWord struct {
	glob, text string
	negated    bool
}

// parseNameMatch reads the Name= of the [Match] sections of the Network document network's spec
// as systemd does: the values in order, each split into words at spaces, tabs and line breaks, a
// value of only those dropping the words before it. A value whose first word starts with !
// negates all of its words, and in another value a word that starts with ! negates itself.
// systemd drops a word that is no interface name, and so does parseNameMatch. It refuses a
// backslash, which systemd reads as an escape when it splits the value, and a word it cannot
// match as fnmatch does. It reads interface names alone: networkd also matches a word against a
// link's alternative names, which the documents do not give.
func parseNameMatch(network string, spec map[string]any) (nameMatch, error) {
	var m nameMatch
	for _, s := range oneOrMany(spec["match"]) {
		section, _ := s.(map[string]any)
		for _, v := range oneOrMany(section["name"]) {
			value, err := unitValue(v)
			if err != nil {
				continue
			}
			words := strings.FieldsFunc(value, func(r rune) bool { return strings.ContainsRune(unitWhitespace, r) })
			if len(words) == 0 {
				m.words = nil
				continue
			}
			inverted := strings.HasPrefix(words[0], "!")
			if inverted {
				words[0] = words[0][1:]
			}
			for _, w := range words {
				if strings.Contains(w, `\`) {
					return m, fmt.Errorf("Network %s: Name= holds %q, whose backslash networkd reads as an escape; name the links without one", network, w)
				}
				if !systemdIfname(w) {
					continue
				}
				word := nameWord{text: w, negated: inverted}
				if !inverted {
					w, word.negated = strings.CutPrefix(w, "!")
				}
				word.glob = goGlob(w)
				if _, err := path.Match(word.glob, ""); err != nil || bracketHolds(w, '[') {
					return m, fmt.Errorf("Network %s: Name= holds %q, which is no glob the renderer can match as networkd does", network, word.text)
				}
				m.words = append(m.words, word)
			}
		}
	}
	return m, nil
}

// unitWhitespace are the characters systemd splits a value at, and strips around it.
const unitWhitespace = " \t\n\r"

// systemdIfname reports whether systemd takes a word of Name= for an interface name or glob; it
// drops the others with a warning.
func systemdIfname(word string) bool {
	if word == "" || ifindex(word) || len(word) >= 128 || word == "." || word == ".." || word == "all" || word == "default" {
		return false
	}
	numeric := true
	for i := range len(word) {
		c := word[i]
		if c <= ' ' || c >= 127 || c == ':' || c == '/' || c == '%' {
			return false
		}
		numeric = numeric && c >= '0' && c <= '9'
	}
	return !numeric
}

// ifindex reports whether systemd reads a word as an interface index: an integer from 1 that
// fits an int, as strtol reads it in base 0, after a prefix 0b or 0o that sets base 2 or 8.
func ifindex(word string) bool {
	base := uint64(0)
	if len(word) > 2 && word[0] == '0' {
		switch word[1] {
		case 'b', 'B':
			base, word = 2, word[2:]
		case 'o', 'O':
			base, word = 8, word[2:]
		}
	}
	negative := false
	if word != "" && (word[0] == '+' || word[0] == '-') {
		negative, word = word[0] == '-', word[1:]
	}
	digit := func(c byte) uint64 {
		switch {
		case c >= '0' && c <= '9':
			return uint64(c - '0')
		case c >= 'a' && c <= 'z':
			return uint64(c-'a') + 10
		case c >= 'A' && c <= 'Z':
			return uint64(c-'A') + 10
		}
		return 36
	}
	prefixed := func(lower byte, b uint64) bool {
		return (base == 0 || base == b) && len(word) > 2 && word[0] == '0' && word[1]|0x20 == lower && digit(word[2]) < b
	}
	switch {
	case prefixed('x', 16):
		base, word = 16, word[2:]
	case prefixed('b', 2):
		base, word = 2, word[2:]
	case base == 0 && strings.HasPrefix(word, "0"):
		base = 8
	case base == 0:
		base = 10
	}
	if word == "" {
		return false
	}
	n := uint64(0)
	for i := range len(word) {
		d := digit(word[i])
		if d >= base {
			return false
		}
		if n = n*base + d; n > math.MaxInt32 {
			return false
		}
	}
	return !negative && n > 0
}

// goGlob is the fnmatch glob as path.Match takes it, which negates a bracket expression with ^
// where fnmatch takes !.
func goGlob(glob string) string {
	return strings.ReplaceAll(glob, "[!", "[^")
}

// bracketHolds reports whether a bracket expression of the glob holds c. The bracket
// expressions of fnmatch read [ as the start of a class such as [:digit:], which path.Match
// takes for the characters themselves.
func bracketHolds(glob string, c byte) bool {
	in := false
	for i := 0; i < len(glob); i++ {
		switch {
		case !in && glob[i] == '[':
			in = true
			if i+1 < len(glob) && (glob[i+1] == '!' || glob[i+1] == '^') {
				i++
			}
			if i+1 < len(glob) && glob[i+1] == ']' {
				i++
			}
		case in && glob[i] == ']':
			in = false
		case in && glob[i] == c:
			return true
		}
	}
	return false
}

// matches reports whether networkd matches the link by this Name=.
func (m nameMatch) matches(link string) bool {
	positive, matched := false, false
	for _, w := range m.words {
		ok, _ := path.Match(w.glob, link)
		if w.negated {
			if ok {
				return false
			}
			continue
		}
		positive = true
		matched = matched || ok
	}
	return !positive || matched
}

// positives are the words that are not negated, or a word * when there are none, as the Name=
// then matches every link the negated words leave.
func (m nameMatch) positives() []nameWord {
	var p []nameWord
	for _, w := range m.words {
		if !w.negated {
			p = append(p, w)
		}
	}
	if len(p) == 0 {
		return []nameWord{{glob: "*"}}
	}
	return p
}

// mayMatch reports whether the Name= may match a link the glob matches. It reads the negated
// words of the Name= only against a literal glob, so it may say so of a glob whose links the
// negated words exclude.
func (m nameMatch) mayMatch(glob string) bool {
	if isLiteral(glob) {
		return m.matches(glob)
	}
	return slices.ContainsFunc(m.positives(), func(w nameWord) bool { return globsOverlap(w.glob, glob) })
}

// covers reports whether the Name= matches every link the glob matches, as globContains finds.
func (m nameMatch) covers(glob string) bool {
	if isLiteral(glob) {
		return m.matches(glob)
	}
	for _, w := range m.words {
		if w.negated && globsOverlap(w.glob, glob) {
			return false
		}
	}
	return slices.ContainsFunc(m.positives(), func(w nameWord) bool { return globContains(w.glob, glob) })
}

// globPrefix is the part of a glob, as path.Match takes it, before its first special character.
func globPrefix(glob string) string {
	if i := strings.IndexAny(glob, `*?[\`); i >= 0 {
		return glob[:i]
	}
	return glob
}

// isLiteral reports whether a glob matches only itself.
func isLiteral(glob string) bool {
	return globPrefix(glob) == glob
}

// globsOverlap reports whether two globs may match one name: two literal ones when they are
// equal, a literal and a glob when the glob matches it, and two globs unless their literal
// beginnings differ.
func globsOverlap(a, b string) bool {
	switch {
	case isLiteral(a):
		ok, _ := path.Match(b, a)
		return ok
	case isLiteral(b):
		ok, _ := path.Match(a, b)
		return ok
	}
	pa, pb := globPrefix(a), globPrefix(b)
	return strings.HasPrefix(pa, pb) || strings.HasPrefix(pb, pa)
}

// globContains reports whether outer matches every name inner matches: a literal inner that
// outer matches, an inner that is outer, or an inner that starts with what an outer of a literal
// beginning and * starts with. Other globs it takes for not contained.
func globContains(outer, inner string) bool {
	if isLiteral(inner) {
		ok, _ := path.Match(outer, inner)
		return ok
	}
	prefix := globPrefix(outer)
	return outer == inner || outer == prefix+"*" && strings.HasPrefix(globPrefix(inner), prefix)
}

// decodeKind decodes the specs of the documents of kind in byKind, in their order.
func decodeKind[T any](byKind map[string]any, kind string) ([]named[T], error) {
	docs, _ := byKind[kind].([]any)
	out := make([]named[T], 0, len(docs))
	for _, d := range docs {
		obj, _ := d.(map[string]any)
		name, err := documentName(kind, obj)
		if err != nil {
			return nil, err
		}
		if !routerName.MatchString(name) {
			return nil, fmt.Errorf("a %s document: %q is no document name, which nftables takes in the names of sets and chains", kind, name)
		}
		data, err := json.Marshal(obj["spec"])
		if err != nil {
			return nil, err
		}
		var spec T
		if err := json.Unmarshal(data, &spec); err != nil {
			return nil, fmt.Errorf("%s %s: %w", kind, name, err)
		}
		out = append(out, named[T]{name, spec})
	}
	return out, nil
}

// router is the node's Router, or the kind's defaults when its config holds none.
func (d routerDocuments) router() routerSpec {
	if len(d.Routers) == 0 {
		return defaultRouter()
	}
	return d.Routers[0].Spec
}

// vrfOf is the VRF a document's vrf names: its name, or the default VRF's when it is null.
func vrfOf(v *string) string {
	if v == nil {
		return defaultVRF
	}
	return *v
}

// vrfOrder sorts the default VRF before the others, which follow by name.
func vrfOrder(vrf string) string {
	if vrf == defaultVRF {
		return ""
	}
	return vrf
}

// routerName is a document's name as the schemas take it, which the ruleset writes unquoted in
// the names of its sets and chains.
var routerName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// interfaceName is an interface name as the schemas take it, or a prefix of one with the one
// trailing * that nftables matches.
var interfaceName = regexp.MustCompile(`^(?:[A-Za-z0-9_.:@-]{1,15}|[A-Za-z0-9_.:@-]{0,14}[*])$`)

// checkInterfaces refuses an empty list of interfaces and a name or glob the schemas refuse.
func checkInterfaces(what string, l []string) error {
	if len(l) == 0 {
		return fmt.Errorf("%s names no interface", what)
	}
	for _, n := range l {
		if !interfaceName.MatchString(n) {
			return fmt.Errorf("%s: %q is no interface name or glob", what, n)
		}
	}
	return nil
}
