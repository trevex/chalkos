package main

import (
	"encoding/json"
	"fmt"
	"maps"
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
	ASN       uint32 `json:"asn"`
	RouterID  string `json:"routerID"`
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
}

type peerSpec struct {
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
	// Links are the interface names the Network documents' [Match] sections name, sorted.
	Links []string
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
	d.Links = networkLinks(byKind)
	return d, nil
}

// networkLinks are the interface names in the [Match] sections' Name= of the Network documents
// of byKind, sorted and once each. A value, or each of a list, holds names between spaces and
// tabs, as systemd splits it; a glob or a negation, starting with !, names no link.
func networkLinks(byKind map[string]any) []string {
	docs, _ := byKind["Network"].([]any)
	seen := map[string]bool{}
	for _, d := range docs {
		doc, _ := d.(map[string]any)
		spec, _ := doc["spec"].(map[string]any)
		for _, m := range oneOrMany(spec["match"]) {
			section, _ := m.(map[string]any)
			for _, v := range oneOrMany(section["name"]) {
				value, _ := v.(string)
				for _, name := range strings.Fields(value) {
					if !strings.ContainsAny(name, "*?[!") && interfaceName.MatchString(name) {
						seen[name] = true
					}
				}
			}
		}
	}
	return slices.Sorted(maps.Keys(seen))
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
