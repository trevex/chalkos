package config

import (
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/schema"
)

// routerSchemas are the router kinds' schemas, as a router image generates them.
func routerSchemas(t *testing.T) *schema.Schemas {
	t.Helper()
	s, err := schema.LoadSchemas("../../test/fixtures/kinds/router")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// routerDoc is a router document of the kind with the spec given in YAML flow style.
func routerDoc(kind, name, spec string) string {
	return "---\napiVersion: " + RouterAPI + "\nkind: " + kind + "\nmetadata: {name: " + name + "}\nspec: " + spec + "\n"
}

// checkRouterRefs checks the references of router documents, with their schema defaults filled
// in as NewSet fills them in before it checks references.
func checkRouterRefs(t *testing.T, yaml string) (map[ID][]Ref, error) {
	t.Helper()
	s := routerSchemas(t)
	docs := parseAll(t, yaml)
	for i, d := range docs {
		filled, err := s.Default(d.Object)
		if err != nil {
			t.Fatal(err)
		}
		docs[i] = FromObject(filled)
	}
	return checkRefs(docs, s)
}

// wantRefused fails t unless err holds the lines want, in order.
func wantRefused(t *testing.T, err error, want ...string) {
	t.Helper()
	if err == nil || err.Error() != strings.Join(want, "\n") {
		t.Errorf("err =\n%v\nwant\n%s", err, strings.Join(want, "\n"))
	}
}

// A BGP instance, a peer, a NAT64, a resolver, an import, a zone and a policy route's target
// name VRF documents. default names the default VRF, which has no document, where a document
// runs in a VRF or an import takes its routes; a zone and a policy route's target name VRF
// documents alone. No VRF document is named default.
func TestRouterVRFRefs(t *testing.T) {
	_, err := checkRouterRefs(t,
		routerDoc("BGP", "tenant", `{vrf: tenant, asn: 64512, routerID: 192.0.2.1, import: {ipv6: {vrfs: [lab, default]}}}`)+
			routerDoc("BGPPeer", "up", `{vrf: tenant, address: "2001:db8::1", remoteAS: external, families: {ipv6: {}}}`)+
			routerDoc("NAT64", "edge", `{vrf: tenant}`)+
			routerDoc("Resolver", "r", `{vrf: tenant, listen: ["2001:db8::1"], allow: ["2001:db8::/32"]}`)+
			routerDoc("FirewallZone", "lan", `{vrfs: [tenant, default]}`)+
			routerDoc("PolicyRoute", "lan", `{rules: [{match: {dscp: [46]}, to: {vrf: default}}]}`))
	wantRefused(t, err,
		`BGP/tenant: spec.import.ipv6.vrfs[0]: VRF "lab" does not exist`,
		`BGP/tenant: spec.vrf: VRF "tenant" does not exist`,
		`BGPPeer/up: spec.vrf: VRF "tenant" does not exist`,
		`FirewallZone/lan: spec.vrfs[0]: VRF "tenant" does not exist`,
		`FirewallZone/lan: spec.vrfs[1]: VRF "default" does not exist`,
		`NAT64/edge: spec.vrf: VRF "tenant" does not exist`,
		`PolicyRoute/lan: spec.rules[0].to.vrf: VRF "default" does not exist`,
		`Resolver/r: spec.vrf: VRF "tenant" does not exist`)

	refs, err := checkRouterRefs(t,
		routerDoc("BGP", "bgp", `{asn: 64512, routerID: 192.0.2.1}`)+
			routerDoc("BGP", "tenant", `{vrf: tenant, asn: 64512, routerID: 192.0.2.1, import: {ipv6: {vrfs: [default]}}}`)+
			routerDoc("BGPPeer", "up", `{vrf: default, address: "2001:db8::1", remoteAS: external, families: {ipv6: {}}}`)+
			routerDoc("VRF", "tenant", `{table: 100}`))
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[ID]int{
		{APIVersion: RouterAPI, Kind: "BGP", Name: "bgp"}:    0,
		{APIVersion: RouterAPI, Kind: "BGPPeer", Name: "up"}: 0,
		{APIVersion: RouterAPI, Kind: "BGP", Name: "tenant"}: 1,
		{APIVersion: RouterAPI, Kind: "VRF", Name: "tenant"}: 0,
	} {
		if got := refs[id]; len(got) != want {
			t.Errorf("%s references %v, want %d", id, got, want)
		}
	}

	d := parseOne(t, routerDoc("VRF", "default", `{table: 100}`))
	if err := routerSchemas(t).Validate(d.Object); err == nil || !strings.Contains(err.Error(), `the name "default" is reserved`) {
		t.Errorf("a VRF named default: err = %v, want the name refused as reserved", err)
	}
}

// A FirewallPolicy, a NAT rule's place and a policy route's in name FirewallZone documents. local
// names the router itself, which has no document, in a FirewallPolicy alone, and no FirewallZone
// is named local.
func TestRouterZoneRefs(t *testing.T) {
	_, err := checkRouterRefs(t,
		routerDoc("FirewallPolicy", "lan-wan", `{from: lan, to: wan}`)+
			routerDoc("FirewallPolicy", "dmz-local", `{from: dmz, to: local}`)+
			routerDoc("NATRule", "out", `{type: masquerade, out: {zone: wan}}`)+
			routerDoc("NATRule", "web", `{type: dnat, in: {zone: local}, to: {address: 192.0.2.10}}`)+
			routerDoc("PolicyRoute", "lan", `{in: {zone: dmz}, rules: [{match: {dscp: [46]}, to: {table: 100}}]}`)+
			routerDoc("FirewallZone", "lan", `{interfaces: [eth1]}`))
	wantRefused(t, err,
		`FirewallPolicy/dmz-local: spec.from: FirewallZone "dmz" does not exist`,
		`FirewallPolicy/lan-wan: spec.to: FirewallZone "wan" does not exist`,
		`NATRule/out: spec.out.zone: FirewallZone "wan" does not exist`,
		`NATRule/web: spec.in.zone: FirewallZone "local" does not exist`,
		`PolicyRoute/lan: spec.in.zone: FirewallZone "dmz" does not exist`)

	d := parseOne(t, routerDoc("FirewallZone", "local", `{interfaces: [lo]}`))
	if err := routerSchemas(t).Validate(d.Object); err == nil || !strings.Contains(err.Error(), `the name "local" is reserved`) {
		t.Errorf("a FirewallZone named local: err = %v, want the name refused as reserved", err)
	}
}

// A BGP instance's networks and imports, a peer's families and a policy's terms name
// RoutingPolicy, PrefixList and CommunityList documents.
func TestRouterBGPRefs(t *testing.T) {
	_, err := checkRouterRefs(t,
		routerDoc("BGP", "bgp", `{asn: 64512, routerID: 192.0.2.1, networks: [{prefix: "2001:db8::/32", policy: out}], import: {ipv6: {vrfs: [default], policy: in}}}`)+
			routerDoc("BGPPeer", "up", `{address: "2001:db8::1", remoteAS: external, families: {ipv6: {import: in, export: out, defaultOriginate: true, defaultOriginateWhen: up}}}`)+
			routerDoc("RoutingPolicy", "p", `{terms: [{match: {prefixList: v6, communityList: drop}, then: {action: accept}}]}`))
	wantRefused(t, err,
		`BGP/bgp: spec.import.ipv6.policy: RoutingPolicy "in" does not exist`,
		`BGP/bgp: spec.networks[0].policy: RoutingPolicy "out" does not exist`,
		`BGPPeer/up: spec.families.ipv6.defaultOriginateWhen: PrefixList "up" does not exist`,
		`BGPPeer/up: spec.families.ipv6.export: RoutingPolicy "out" does not exist`,
		`BGPPeer/up: spec.families.ipv6.import: RoutingPolicy "in" does not exist`,
		`RoutingPolicy/p: spec.terms[0].match.communityList: CommunityList "drop" does not exist`,
		`RoutingPolicy/p: spec.terms[0].match.prefixList: PrefixList "v6" does not exist`)
}
