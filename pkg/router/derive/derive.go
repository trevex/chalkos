// Package derive is the router's model of a node's config: the documents of the router layer's
// kinds and of networkd's Network, decoded with their schema defaults, and what follows from
// them together, which no single document says. That is the links the Network documents name,
// the .network units in the order networkd picks them, the VRF networkd puts each link in, the
// VRFs' devices, the NAT64 instances with their links and addresses, and the tables policy
// routing sends traffic to.
//
// chalkos-render builds the model from an output's input and renders the router's files from
// it, and runs the validator router, Validate, as chalkos-render validate router; chalkctl links
// Validate and runs it in-process on the same input. Since every router image carries
// chalkos-render, the package takes the standard library alone, besides package refusals.
//
// A config the model refuses for what no schema or rule sees is a *refusals.Error that names the
// document and its field. A guard of what the schemas and rules refuse before chalkd renders is
// an internal error, as InternalErrorf makes it.
package derive

import (
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Kinds are the kinds whose documents Build reads: the router layer's and networkd's Network. An
// image declares those of its router features; the validator router reads the documents of the
// kinds of Kinds that the image declares, on the node as in chalkctl.
var Kinds = []string{
	"Router", "FirewallZone", "FirewallPolicy", "AddressSet", "NATRule", "BGP", "BGPPeer", "PrefixList",
	"CommunityList", "RoutingPolicy", "NAT64", "Resolver", "VRF", "PolicyRoute", "NextHop", "DHCPSubnet",
	"DHCPRelay", "Network",
}

// Named is the spec of a document, with the document's name.
type Named[T any] struct {
	Name string
	Spec T
}

// Model is the router's model of a node's config, as Build derives it from the documents of an
// output's input or a validator's: the router kinds' documents, each kind's in the input's order,
// which sorts them by name, and what the Network and VRF documents say together. The parts that
// only some outputs need, and that refuse what the others need not see, are methods: Units,
// NAT64Instances and PolicyTables.
type Model struct {
	Routers  []Named[RouterSpec]
	Zones    []Named[ZoneSpec]
	Policies []Named[PolicySpec]
	Sets     []Named[AddressSetSpec]
	NAT      []Named[NATSpec]
	BGP      []Named[BGPSpec]
	Peers    []Named[PeerSpec]
	Prefixes []Named[PrefixListSpec]
	// Communities are the CommunityList documents and RoutingPolicies the RoutingPolicy ones.
	Communities     []Named[CommunityListSpec]
	RoutingPolicies []Named[RoutingPolicySpec]
	NAT64           []Named[NAT64Spec]
	Resolvers       []Named[ResolverSpec]
	VRFs            []Named[VRFSpec]
	PolicyRoutes    []Named[PolicyRouteSpec]
	NextHops        []Named[NextHopSpec]
	DHCPSubnets     []Named[DHCPSubnetSpec]
	DHCPRelays      []Named[DHCPRelaySpec]
	// Links are the interface names the Network documents' [Match] sections name, sorted.
	Links []string
	// Addresses are the static addresses the Network documents give their links.
	Addresses []NetworkAddress
	// Networks are the Network documents' specs, by name.
	Networks map[string]map[string]any
	// Membership is the VRF of the links of Links and of the Network documents.
	Membership Membership
	// byKind are the documents the model was built of, by kind; Units reads their Network
	// documents.
	byKind map[string]any
}

// Build is the model of byKind, the documents of an output's input by kind, as JSON decodes them
// with their numbers exact; a kind the input lacks has no documents. It refuses what vrfLinks
// refuses, and guards a document whose spec does not decode into its kind's type or whose name is
// none the schemas take.
func Build(byKind map[string]any) (Model, error) {
	d := Model{byKind: byKind}
	var err error
	if d.Routers, err = decodeKind[RouterSpec](byKind, "Router"); err != nil {
		return d, err
	}
	if d.Zones, err = decodeKind[ZoneSpec](byKind, "FirewallZone"); err != nil {
		return d, err
	}
	if d.Policies, err = decodeKind[PolicySpec](byKind, "FirewallPolicy"); err != nil {
		return d, err
	}
	if d.Sets, err = decodeKind[AddressSetSpec](byKind, "AddressSet"); err != nil {
		return d, err
	}
	if d.NAT, err = decodeKind[NATSpec](byKind, "NATRule"); err != nil {
		return d, err
	}
	if d.BGP, err = decodeKind[BGPSpec](byKind, "BGP"); err != nil {
		return d, err
	}
	if d.Peers, err = decodeKind[PeerSpec](byKind, "BGPPeer"); err != nil {
		return d, err
	}
	if d.Prefixes, err = decodeKind[PrefixListSpec](byKind, "PrefixList"); err != nil {
		return d, err
	}
	if d.Communities, err = decodeKind[CommunityListSpec](byKind, "CommunityList"); err != nil {
		return d, err
	}
	if d.RoutingPolicies, err = decodeKind[RoutingPolicySpec](byKind, "RoutingPolicy"); err != nil {
		return d, err
	}
	if d.NAT64, err = decodeKind[NAT64Spec](byKind, "NAT64"); err != nil {
		return d, err
	}
	if d.Resolvers, err = decodeKind[ResolverSpec](byKind, "Resolver"); err != nil {
		return d, err
	}
	if d.VRFs, err = decodeKind[VRFSpec](byKind, "VRF"); err != nil {
		return d, err
	}
	if d.PolicyRoutes, err = decodeKind[PolicyRouteSpec](byKind, "PolicyRoute"); err != nil {
		return d, err
	}
	if d.NextHops, err = decodeKind[NextHopSpec](byKind, "NextHop"); err != nil {
		return d, err
	}
	if d.DHCPSubnets, err = decodeKind[DHCPSubnetSpec](byKind, "DHCPSubnet"); err != nil {
		return d, err
	}
	if d.DHCPRelays, err = decodeKind[DHCPRelaySpec](byKind, "DHCPRelay"); err != nil {
		return d, err
	}
	d.Links = networkLinks(byKind)
	d.Addresses = networkAddresses(byKind)
	d.Networks = networkSpecs(byKind)
	if d.Membership, err = vrfLinks(d.VRFs, d.Links, byKind); err != nil {
		return d, err
	}
	return d, nil
}

// decodeKind decodes the specs of the documents of kind in byKind, in their order.
func decodeKind[T any](byKind map[string]any, kind string) ([]Named[T], error) {
	docs, _ := byKind[kind].([]any)
	out := make([]Named[T], 0, len(docs))
	for _, d := range docs {
		obj, _ := d.(map[string]any)
		name, err := DocumentName(kind, obj)
		if err != nil {
			return nil, err
		}
		if !routerName.MatchString(name) {
			return nil, InternalErrorf("a %s document: %q is no document name, which nftables takes in the names of sets and chains", kind, name)
		}
		data, err := json.Marshal(obj["spec"])
		if err != nil {
			return nil, err
		}
		// The schemas give each field the type of its Go field, so a spec that does not decode is
		// one chalkd did not check.
		var spec T
		if err := json.Unmarshal(data, &spec); err != nil {
			return nil, InternalErrorf("%s %s: %w", kind, name, err)
		}
		out = append(out, Named[T]{name, spec})
	}
	return out, nil
}

// DocumentName is the name of doc, a document of kind, which names a file of its own. Every
// kind's schema holds metadata.name to a pattern without / or a leading dot, so a name that would
// leave the output's directory is an internal error.
func DocumentName(kind string, doc map[string]any) (string, error) {
	meta, _ := doc["metadata"].(map[string]any)
	name, _ := meta["name"].(string)
	if name == "" || strings.ContainsAny(name, "/\x00") || name == "." || name == ".." {
		return "", InternalErrorf("a %s document has no name to name its file by", kind)
	}
	return name, nil
}

// routerName is a document's name as the schemas take it, which the ruleset writes unquoted in
// the names of its sets and chains.
var routerName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// ByName are the specs of docs by their documents' names.
func ByName[T any](docs []Named[T]) map[string]T {
	m := make(map[string]T, len(docs))
	for _, d := range docs {
		m[d.Name] = d.Spec
	}
	return m
}

// Router is the node's Router, or the kind's defaults when its config holds none.
func (d Model) Router() RouterSpec {
	if len(d.Routers) == 0 {
		return defaultRouter()
	}
	return d.Routers[0].Spec
}

// defaultRouter is the Router of a node whose config holds none: the kind's defaults.
func defaultRouter() RouterSpec {
	var r RouterSpec
	r.Forwarding.IPv4, r.Forwarding.IPv6 = true, true
	r.RPFilter, r.ECMPHash = "loose", "l4"
	return r
}

// VRFOf is the VRF a document's vrf names: its name, which is default for the default VRF, or the
// default VRF's when the input leaves it out.
func VRFOf(v *string) string {
	if v == nil {
		return DefaultVRF
	}
	return *v
}

// VRFOfNetwork is the VRF of the links of the Network document named n.
func (d Model) VRFOfNetwork(n string) string {
	if vrf, ok := d.Membership.Networks[n]; ok {
		return vrf
	}
	return DefaultVRF
}

// Devices are the VRFs' devices, sorted: those of the VRF documents and of the Networks' own
// VRF=.
func (d Model) Devices() []string {
	devices := map[string]bool{}
	for _, v := range d.VRFs {
		devices[v.Name] = true
	}
	for _, m := range d.Membership.Members {
		devices[m.VRF] = true
	}
	return slices.Sorted(maps.Keys(devices))
}
