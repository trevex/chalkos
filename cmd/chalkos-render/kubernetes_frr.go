package main

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
)

// The Kubernetes layer's BGP, which the output bgp renders from the BGPSpeaker, BGPPeer and Node
// documents into FRR's config.
const (
	// bgpGroupPrefix starts a BGPPeer's peer group, peer-<name>, whose password line frr.service's
	// fill of the credentials matches.
	bgpGroupPrefix = "peer-"
	// bgpMaxPaths is how many equal paths to a prefix, such as a default route from two sessions,
	// zebra installs.
	bgpMaxPaths = 16
	// bgpConnectRetry is the seconds between FRR's attempts to open a session.
	bgpConnectRetry = 10
)

// bgpLists are the lists every speaker's frr.conf holds: the default routes an import of default
// takes, and the empty AS path of the routes the node originates, which alone it announces.
const bgpLists = "ip prefix-list default-ipv4 seq 5 permit 0.0.0.0/0\n" +
	"ipv6 prefix-list default-ipv6 seq 5 permit ::/0\n" +
	"!\n" +
	"bgp as-path access-list local seq 5 permit ^$\n" +
	"!\n" +
	"route-map announce permit 10\n match as-path local\nexit\n"

// bgpImports are the route-maps of a peer's import: its default routes of a family, or nothing.
const bgpImports = "route-map import-ipv4 permit 10\n match ip address prefix-list default-ipv4\nexit\n" +
	"route-map import-ipv6 permit 10\n match ipv6 address prefix-list default-ipv6\nexit\n" +
	"route-map import-none deny 10\nexit\n" +
	"!\n"

// bgpDocument is a document of the input: its name and its spec.
type bgpDocument[T any] struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec T `json:"spec"`
}

type bgpSpeaker struct {
	ASN      uint32  `json:"asn"`
	RouterID *string `json:"routerID"`
	Export   struct {
		Interfaces []string `json:"interfaces"`
	} `json:"export"`
}

type bgpPeer struct {
	ASN        uint32   `json:"asn"`
	Address    *string  `json:"address"`
	Interfaces []string `json:"interfaces"`
	Password   *struct {
		SecretRef struct {
			Name string `json:"name"`
			Key  string `json:"key"`
		} `json:"secretRef"`
	} `json:"password"`
	BFD    bool   `json:"bfd"`
	Import string `json:"import"`
}

type bgpNode struct {
	NodeIP struct {
		Addresses []string `json:"addresses"`
	} `json:"nodeIP"`
}

// bgpSession is a BGPPeer as frr.conf runs it: its peer group, its address, unset for unnumbered
// sessions, and its password's placeholder, empty for none.
type bgpSession struct {
	group, password string
	address         netip.Addr
	spec            bgpPeer
}

// carries tells whether the session exchanges routes of the family: an unnumbered session and an
// IPv6 one bound to an interface carry both, IPv4 routes with IPv6 next hops; another carries
// its address's family alone.
func (s bgpSession) carries(family string) bool {
	switch {
	case !s.address.IsValid() || s.extendedNextHop():
		return true
	case s.address.Is4():
		return family == "ipv4"
	}
	return family == "ipv6"
}

// extendedNextHop tells whether the session to an IPv6 address carries IPv4 routes with IPv6 next
// hops, which FRR negotiates on an unnumbered session by itself. It does so on a session bound to
// an interface alone, on whose link FRR's router advertisements offer no default router.
func (s bgpSession) extendedNextHop() bool {
	return s.address.Is6() && len(s.spec.Interfaces) == 1
}

// bgpInterface is an interface's name as frr.conf writes it, unquoted.
var bgpInterface = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

// bgpPeerName is a BGPPeer's name as its peer group takes it, which frr.service's fill reads
// with the same characters.
var bgpPeerName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// bgpInternalf is the error of a guard of what the Kubernetes layer's schemas and rules refuse
// before chalkd renders.
func bgpInternalf(format string, args ...any) error {
	return fmt.Errorf("internal error: "+format+"; chalkd checks the config against the schemas and the rules before it renders", args...)
}

// decodeDocuments decodes the input's documents of the kind; a kind the input lacks has none.
func decodeDocuments[T any](in map[string]any, kind string) ([]bgpDocument[T], error) {
	data, err := json.Marshal(documents(in)[kind])
	if err != nil {
		return nil, err
	}
	var docs []bgpDocument[T]
	if err := json.Unmarshal(data, &docs); err != nil {
		return nil, fmt.Errorf("read the %s documents: %w", kind, err)
	}
	return docs, nil
}

// renderKubernetesFRR writes frr.conf to out: a BGP instance of the BGPSpeaker's AS with a peer
// group per BGPPeer, the connected routes of the speaker's export interfaces announced to every
// peer, and the default routes each peer's import takes. A password is a placeholder,
// @secret_<secret>.<key>@, that frr.service fills in from its credentials. Without a BGPSpeaker
// the node runs no BGP.
func renderKubernetesFRR(in map[string]any, out string) error {
	speakers, err := decodeDocuments[bgpSpeaker](in, "BGPSpeaker")
	if err != nil {
		return err
	}
	peers, err := decodeDocuments[bgpPeer](in, "BGPPeer")
	if err != nil {
		return err
	}
	nodes, err := decodeDocuments[bgpNode](in, "Node")
	if err != nil {
		return err
	}
	node, _ := in["node"].(map[string]any)
	name, _ := node["name"].(string)
	text, err := kubernetesFRRConfig(name, speakers, peers, nodes)
	if err != nil {
		return err
	}
	return writeFile(out, frrFile, []byte(text))
}

func kubernetesFRRConfig(node string, speakers []bgpDocument[bgpSpeaker], peers []bgpDocument[bgpPeer], nodes []bgpDocument[bgpNode]) (string, error) {
	var b strings.Builder
	writeFRRHeader(&b, "kubernetes-frr", node)
	switch {
	case len(speakers) == 0 && len(peers) > 0:
		return "", bgpInternalf("the config holds BGPPeer documents but no BGPSpeaker")
	case len(speakers) == 0:
		b.WriteString("end\n")
		return b.String(), nil
	case len(speakers) > 1:
		return "", bgpInternalf("the config holds %d BGPSpeaker documents; a node has one", len(speakers))
	}
	s := speakers[0].Spec
	routerID, err := bgpRouterID(s, nodes)
	if err != nil {
		return "", err
	}
	sessions := make([]bgpSession, 0, len(peers))
	// links are the interfaces the sessions run on, unnumbered or bound.
	var links []string
	for _, p := range peers {
		session, err := bgpSessionOf(p)
		if err != nil {
			return "", err
		}
		sessions = append(sessions, session)
		links = append(links, session.spec.Interfaces...)
	}
	b.WriteString(bgpLists)
	for i, iface := range s.Export.Interfaces {
		if !bgpInterface.MatchString(iface) {
			return "", bgpInternalf("BGPSpeaker %s: the export interface %q is no interface name", speakers[0].Metadata.Name, iface)
		}
		fmt.Fprintf(&b, "route-map export permit %d\n match interface %s\nexit\n", 10*(i+1), iface)
	}
	b.WriteString(bgpImports)
	// FRR sends router advertisements on the links of unnumbered sessions, which find the
	// neighbour by them, and of extended next hops; a lifetime of 0 keeps any host on the link
	// from taking the node as its default router.
	if links = slices.Compact(slices.Sorted(slices.Values(links))); len(links) > 0 {
		for _, iface := range links {
			fmt.Fprintf(&b, "interface %s\n ipv6 nd ra-lifetime 0\nexit\n", iface)
		}
		b.WriteString("!\n")
	}
	fmt.Fprintf(&b, "router bgp %d\n bgp router-id %s\n no bgp default ipv4-unicast\n", s.ASN, routerID)
	for _, p := range sessions {
		g := p.group
		fmt.Fprintf(&b, " neighbor %s peer-group\n neighbor %s remote-as %d\n", g, g, p.spec.ASN)
		if p.password != "" {
			fmt.Fprintf(&b, " neighbor %s password %s\n", g, p.password)
		}
		if p.spec.BFD {
			fmt.Fprintf(&b, " neighbor %s bfd\n", g)
		}
		fmt.Fprintf(&b, " neighbor %s timers connect %d\n", g, bgpConnectRetry)
		if p.extendedNextHop() {
			fmt.Fprintf(&b, " neighbor %s capability extended-nexthop\n", g)
		}
		if !p.address.IsValid() {
			for _, iface := range p.spec.Interfaces {
				fmt.Fprintf(&b, " neighbor %s interface peer-group %s\n", iface, g)
			}
			continue
		}
		fmt.Fprintf(&b, " neighbor %s peer-group %s\n", p.address, g)
		for _, iface := range p.spec.Interfaces {
			fmt.Fprintf(&b, " neighbor %s interface %s\n", p.address, iface)
		}
	}
	for _, family := range []string{"ipv4", "ipv6"} {
		fmt.Fprintf(&b, " !\n address-family %s unicast\n  redistribute connected route-map export\n  maximum-paths %d\n", family, bgpMaxPaths)
		for _, p := range sessions {
			if !p.carries(family) {
				continue
			}
			imports := "import-" + family
			if p.spec.Import == "none" {
				imports = "import-none"
			}
			fmt.Fprintf(&b, "  neighbor %s activate\n  neighbor %s route-map %s in\n  neighbor %s route-map announce out\n", p.group, p.group, imports, p.group)
		}
		b.WriteString(" exit-address-family\n")
	}
	b.WriteString("exit\n!\nend\n")
	return b.String(), nil
}

// bgpRouterID is the speaker's router ID, or the first IPv4 address of the Node's nodeIP.
func bgpRouterID(s bgpSpeaker, nodes []bgpDocument[bgpNode]) (netip.Addr, error) {
	if s.RouterID != nil {
		a, err := netip.ParseAddr(*s.RouterID)
		if err != nil || !a.Is4() {
			return netip.Addr{}, bgpInternalf("the BGPSpeaker's routerID %q is no IPv4 address", *s.RouterID)
		}
		return a, nil
	}
	for _, n := range nodes {
		for _, text := range n.Spec.NodeIP.Addresses {
			if a, err := netip.ParseAddr(text); err == nil && a.Is4() {
				return a, nil
			}
		}
	}
	return netip.Addr{}, bgpInternalf("the BGPSpeaker gives no routerID, and the Node no IPv4 address")
}

// bgpSessionOf takes a BGPPeer, guarding what frr.conf writes of it unquoted: its name in the
// peer group's, its interfaces, its address and its password's Secret and key.
func bgpSessionOf(d bgpDocument[bgpPeer]) (bgpSession, error) {
	p := d.Spec
	what := "BGPPeer " + d.Metadata.Name
	if !bgpPeerName.MatchString(d.Metadata.Name) {
		return bgpSession{}, bgpInternalf("%s: the name is no peer group's name", what)
	}
	s := bgpSession{group: bgpGroupPrefix + d.Metadata.Name, spec: p}
	for _, iface := range p.Interfaces {
		if !bgpInterface.MatchString(iface) {
			return bgpSession{}, bgpInternalf("%s: %q is no interface name", what, iface)
		}
	}
	switch {
	case p.Address == nil && len(p.Interfaces) == 0:
		return bgpSession{}, bgpInternalf("%s names neither an address nor interfaces", what)
	case p.Address != nil:
		a, err := netip.ParseAddr(*p.Address)
		if err != nil || a.Zone() != "" || a.IsUnspecified() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsMulticast() {
			return bgpSession{}, bgpInternalf("%s: %q is no neighbour's address", what, *p.Address)
		}
		if len(p.Interfaces) > 1 {
			return bgpSession{}, bgpInternalf("%s names an address and %d interfaces; FRR binds one session to one", what, len(p.Interfaces))
		}
		s.address = a
	}
	if p.Import != "default" && p.Import != "none" {
		return bgpSession{}, bgpInternalf("%s: import %q is neither default nor none", what, p.Import)
	}
	if pw := p.Password; pw != nil {
		c, err := credentialOf(pw.SecretRef.Name, pw.SecretRef.Key)
		if err != nil {
			return bgpSession{}, bgpInternalf("%s: the password's %w", what, err)
		}
		s.password = c + "@"
	}
	return s, nil
}
