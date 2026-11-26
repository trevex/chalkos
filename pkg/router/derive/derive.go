// Package derive is the router's model of a node's config: the router layer's documents and
// networkd's Network, decoded with their schema defaults, and what follows from them together,
// such as the links, the .network units in networkd's order, each link's VRF, the NAT64 instances
// and the policy routing tables.
//
// chalkos-render renders the router's files from the model and runs Validate as chalkos-render
// validate router; chalkctl runs Validate in-process. Every router image carries chalkos-render,
// so the package takes the standard library alone, besides refusals, jsonfile and constants. A
// refusal is a *refusals.Error naming the document and field; a guard of what the schemas and
// rules refuse earlier is an internal error, as InternalErrorf makes it.
package derive

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/constants"
	"github.com/trevex/chalkos/pkg/refusals"
)

// API is the apiVersion of the router layer's kinds.
var API = constants.APIVersion("router")

// Kinds returns the kinds whose documents Build reads: the router layer's and networkd's
// Network. An image declares those of its router features; the validator router reads the
// documents of the kinds of Kinds that the image declares, on the node as in chalkctl.
func Kinds() []string {
	names := make([]string, 0, len(routerKinds)+1)
	for _, k := range routerKinds {
		names = append(names, k.name)
	}
	return append(names, "Network")
}

// routerKind is a router layer kind: its name, its spec's Go type, and how Build decodes its
// documents into the model.
type routerKind struct {
	name   string
	spec   reflect.Type
	decode func(m *Model, byKind map[string]any) error
}

// kindOf is the router kind of the name whose documents Build decodes into field.
func kindOf[T any](name string, field func(*Model) *[]Named[T]) routerKind {
	return routerKind{name, reflect.TypeFor[T](), func(m *Model, byKind map[string]any) (err error) {
		*field(m), err = decodeKind[T](byKind, name)
		return err
	}}
}

// routerKinds are the router layer's kinds, in the order Build decodes them.
var routerKinds = []routerKind{
	kindOf("Router", func(m *Model) *[]Named[RouterSpec] { return &m.Routers }),
	kindOf("FirewallZone", func(m *Model) *[]Named[ZoneSpec] { return &m.Zones }),
	kindOf("FirewallPolicy", func(m *Model) *[]Named[PolicySpec] { return &m.Policies }),
	kindOf("AddressSet", func(m *Model) *[]Named[AddressSetSpec] { return &m.Sets }),
	kindOf("NATRule", func(m *Model) *[]Named[NATSpec] { return &m.NAT }),
	kindOf("BGP", func(m *Model) *[]Named[BGPSpec] { return &m.BGP }),
	kindOf("BGPPeer", func(m *Model) *[]Named[PeerSpec] { return &m.Peers }),
	kindOf("PrefixList", func(m *Model) *[]Named[PrefixListSpec] { return &m.Prefixes }),
	kindOf("CommunityList", func(m *Model) *[]Named[CommunityListSpec] { return &m.Communities }),
	kindOf("RoutingPolicy", func(m *Model) *[]Named[RoutingPolicySpec] { return &m.RoutingPolicies }),
	kindOf("NAT64", func(m *Model) *[]Named[NAT64Spec] { return &m.NAT64 }),
	kindOf("Resolver", func(m *Model) *[]Named[ResolverSpec] { return &m.Resolvers }),
	kindOf("VRF", func(m *Model) *[]Named[VRFSpec] { return &m.VRFs }),
	kindOf("PolicyRoute", func(m *Model) *[]Named[PolicyRouteSpec] { return &m.PolicyRoutes }),
	kindOf("NextHop", func(m *Model) *[]Named[NextHopSpec] { return &m.NextHops }),
	kindOf("DHCPSubnet", func(m *Model) *[]Named[DHCPSubnetSpec] { return &m.DHCPSubnets }),
	kindOf("DHCPRelay", func(m *Model) *[]Named[DHCPRelaySpec] { return &m.DHCPRelays }),
	kindOf("WireGuard", func(m *Model) *[]Named[WireGuardSpec] { return &m.WireGuards }),
	kindOf("WireGuardPeer", func(m *Model) *[]Named[WireGuardPeerSpec] { return &m.WireGuardPeers }),
	kindOf("TimeServer", func(m *Model) *[]Named[TimeServerSpec] { return &m.TimeServers }),
}

// Named is the spec of a document, with the document's name.
type Named[T any] struct {
	Name string
	Spec T
}

// Model is the router's view of a node's config, as Build derives it: each router kind's
// documents in the input's order, sorted by name, and what the Network and VRF documents say
// together. Parts only some outputs need, such as Units, Sessions and Subnets, are methods;
// Validate calls each.
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
	WireGuards      []Named[WireGuardSpec]
	WireGuardPeers  []Named[WireGuardPeerSpec]
	TimeServers     []Named[TimeServerSpec]
	// Links are the interface names the Network documents' [Match] sections name and the
	// WireGuards' links, sorted.
	Links []string
	// Addresses are the static addresses the Network documents give their links.
	Addresses []NetworkAddress
	// Networks are the Network documents' specs, by name.
	Networks map[string]map[string]any
	// Membership is the VRF of the links of Links and of the Network documents.
	Membership Membership
	// networkDocs are the Network documents in the input's order, which Units reads.
	networkDocs []Named[map[string]any]
}

// Build is the model of byKind, an output's input by kind with exact numbers. It refuses what
// vrfLinks refuses and guards a spec that does not decode or a name no schema takes.
func Build(byKind map[string]any) (Model, error) {
	var d Model
	for _, k := range routerKinds {
		if err := k.decode(&d, byKind); err != nil {
			return d, err
		}
	}
	var err error
	if d.networkDocs, err = networkDocs(byKind); err != nil {
		return d, err
	}
	d.Links = networkLinks(d.networkDocs)
	d.Addresses = networkAddresses(d.networkDocs)
	d.Networks = ByName(d.networkDocs)
	wireguards := make([]string, 0, len(d.WireGuards))
	for _, w := range d.WireGuards {
		wireguards = append(wireguards, w.Name)
	}
	if d.Membership, err = vrfLinks(d.VRFs, d.Links, wireguards, d.networkDocs); err != nil {
		return d, err
	}
	// Each WireGuard's link is one of the node's, which its own unit configures.
	for _, w := range wireguards {
		if !slices.Contains(d.Links, w) {
			d.Links = append(d.Links, w)
		}
	}
	slices.Sort(d.Links)
	d.Membership = d.Membership.withWireGuards(d.WireGuards)
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
		// The ruleset writes the name unquoted in the names of its sets and chains.
		if !refusals.DocumentName.MatchString(name) {
			return nil, InternalErrorf("a %s document: %q is no document name, which nftables takes in the names of sets and chains", kind, name)
		}
		data, err := json.Marshal(obj["spec"])
		if err != nil {
			return nil, err
		}
		// The schemas give each field the type of its Go field, and the values its enums, formats
		// and patterns take, so a spec that does not decode, or holds a value checkDecoded
		// refuses, is one chalkd did not check.
		var spec T
		err = json.Unmarshal(data, &spec)
		if err == nil {
			err = checkDecoded("spec", reflect.ValueOf(spec))
		}
		if err != nil {
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

// VRFName is the VRF a document's vrf names, as VRFOf reads it, for an output that writes it in
// a file's or a socket's name or unquoted in its config. It guards, with an internal error, a
// name that is no interface's: a VRF document's name holds the characters Build takes alone.
func VRFName(v *string) (string, error) {
	vrf := VRFOf(v)
	if !PlainInterface.MatchString(vrf) {
		return "", InternalErrorf("the VRF %q is no VRF's name", vrf)
	}
	return vrf, nil
}

// vrfOfNetwork is the VRF of the links of the Network document named n.
func (d Model) vrfOfNetwork(n string) string {
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
