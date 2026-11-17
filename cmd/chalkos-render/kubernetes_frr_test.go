package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// kubernetesInput is the input of the output bgp for the node k1 with the documents by kind.
func kubernetesInput(t *testing.T, docs map[string]any) string {
	t.Helper()
	input, err := json.Marshal(map[string]any{"node": map[string]any{"name": "k1"}, "documents": docs})
	if err != nil {
		t.Fatal(err)
	}
	return string(input)
}

// doc is a document of the kind's spec, with its schema's defaults as chalkd fills them in.
func doc(name string, spec map[string]any) map[string]any {
	return map[string]any{"metadata": map[string]any{"name": name}, "spec": spec}
}

func speaker(spec map[string]any) []any { return []any{doc("bgp", spec)} }

// An unnumbered peer on two interfaces: a session on each, both families, BFD, the default
// routes in, what the node originates out, the connected routes of the export interface, and
// router advertisements on both interfaces that offer no default router.
func TestKubernetesFRRUnnumbered(t *testing.T) {
	got := renderedFile(t, []string{"kubernetes-frr"}, kubernetesInput(t, map[string]any{
		"BGPSpeaker": speaker(map[string]any{"asn": 65001, "routerID": "0.0.0.11", "export": map[string]any{"interfaces": []string{"node0"}}}),
		"BGPPeer":    []any{doc("fabric", map[string]any{"asn": 65000, "address": nil, "interfaces": []string{"fabric1", "fabric0"}, "password": nil, "bfd": true, "import": "default"})},
	}), frrFile)
	want := `! FRR's config, rendered by chalkos-render kubernetes-frr. frr.service fills in the passwords, and
! chalkos-frr-reload applies the changes.
frr defaults traditional
hostname k1
log syslog informational
service integrated-vtysh-config
!
ip prefix-list default-ipv4 seq 5 permit 0.0.0.0/0
ipv6 prefix-list default-ipv6 seq 5 permit ::/0
!
bgp as-path access-list local seq 5 permit ^$
!
route-map announce permit 10
 match as-path local
exit
route-map export permit 10
 match interface node0
exit
route-map import-ipv4 permit 10
 match ip address prefix-list default-ipv4
exit
route-map import-ipv6 permit 10
 match ipv6 address prefix-list default-ipv6
exit
route-map import-none deny 10
exit
!
interface fabric0
 ipv6 nd ra-lifetime 0
exit
interface fabric1
 ipv6 nd ra-lifetime 0
exit
!
router bgp 65001
 bgp router-id 0.0.0.11
 no bgp default ipv4-unicast
 neighbor peer-fabric peer-group
 neighbor peer-fabric remote-as 65000
 neighbor peer-fabric bfd
 neighbor peer-fabric timers connect 10
 neighbor fabric1 interface peer-group peer-fabric
 neighbor fabric0 interface peer-group peer-fabric
 !
 address-family ipv4 unicast
  redistribute connected route-map export
  maximum-paths 16
  neighbor peer-fabric activate
  neighbor peer-fabric route-map import-ipv4 in
  neighbor peer-fabric route-map announce out
 exit-address-family
 !
 address-family ipv6 unicast
  redistribute connected route-map export
  maximum-paths 16
  neighbor peer-fabric activate
  neighbor peer-fabric route-map import-ipv6 in
  neighbor peer-fabric route-map announce out
 exit-address-family
exit
!
end
`
	if got != want {
		t.Errorf("frr.conf:\n%s\nwant:\n%s", got, want)
	}
}

// An IPv4 peer carries IPv4 alone; an IPv6 peer bound to an interface carries both with extended
// next hops and keeps the interface's router advertisements from offering a default router; an
// IPv6 peer without an interface, a second switch on eth0's subnet, carries IPv6 alone, without
// extended next hops; a password is a placeholder; a speaker without a router ID takes the Node's
// first IPv4 address.
func TestKubernetesFRRAddresses(t *testing.T) {
	got := renderedFile(t, []string{"kubernetes-frr"}, kubernetesInput(t, map[string]any{
		"Node":       []any{doc("default", map[string]any{"nodeIP": map[string]any{"addresses": []string{"2001:db8::11", "192.0.2.11"}}})},
		"BGPSpeaker": speaker(map[string]any{"asn": 65001, "routerID": nil, "export": map[string]any{"interfaces": []string{"eth0"}}}),
		"BGPPeer": []any{
			doc("tor-v4", map[string]any{"asn": 65000, "address": "192.0.2.1", "interfaces": nil, "password": nil, "bfd": false, "import": "none"}),
			doc("tor-v6", map[string]any{"asn": 65000, "address": "2001:db8::1", "interfaces": []string{"eth0"}, "password": map[string]any{"secretRef": map[string]any{"name": "bgp", "key": "password"}}, "bfd": true, "import": "default"}),
			doc("tor-b", map[string]any{"asn": 65002, "address": "2001:db8::2", "interfaces": nil, "password": nil, "bfd": true, "import": "default"}),
		},
	}), frrFile)
	for _, want := range []string{
		"exit\n!\ninterface eth0\n ipv6 nd ra-lifetime 0\nexit\n!\nrouter bgp 65001\n",
		" bgp router-id 192.0.2.11\n",
		" neighbor peer-tor-v4 remote-as 65000\n neighbor peer-tor-v4 disable-connected-check\n neighbor peer-tor-v4 timers connect 10\n neighbor 192.0.2.1 peer-group peer-tor-v4\n",
		" neighbor peer-tor-v6 password @secret_bgp.password@\n neighbor peer-tor-v6 bfd\n neighbor peer-tor-v6 disable-connected-check\n neighbor peer-tor-v6 timers connect 10\n neighbor peer-tor-v6 capability extended-nexthop\n neighbor 2001:db8::1 peer-group peer-tor-v6\n neighbor 2001:db8::1 interface eth0\n",
		" neighbor peer-tor-b disable-connected-check\n neighbor peer-tor-b timers connect 10\n neighbor 2001:db8::2 peer-group peer-tor-b\n !\n",
		"  neighbor peer-tor-v4 activate\n  neighbor peer-tor-v4 route-map import-none in\n",
		"  neighbor peer-tor-v6 activate\n  neighbor peer-tor-v6 route-map import-ipv4 in\n",
		" address-family ipv6 unicast\n  redistribute connected route-map export\n  maximum-paths 16\n  neighbor peer-tor-v6 activate\n",
		"  neighbor peer-tor-b activate\n  neighbor peer-tor-b route-map import-ipv6 in\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("frr.conf lacks:\n%s\nin:\n%s", want, got)
		}
	}
	ipv4, ipv6, _ := strings.Cut(got, " address-family ipv6")
	if strings.Contains(got, "peer-tor-v4 capability") || strings.Contains(ipv6, "peer-tor-v4") {
		t.Errorf("the IPv4 peer has extended next hops or IPv6 routes:\n%s", got)
	}
	if strings.Contains(got, "peer-tor-b capability") || strings.Contains(ipv4[strings.Index(ipv4, " address-family ipv4"):], "peer-tor-b") {
		t.Errorf("the IPv6 peer without an interface has extended next hops or IPv4 routes:\n%s", got)
	}
}

// An interface that sessions of several peers run on takes one interface block.
func TestKubernetesFRRInterfacesOnce(t *testing.T) {
	got := renderedFile(t, []string{"kubernetes-frr"}, kubernetesInput(t, map[string]any{
		"BGPSpeaker": speaker(map[string]any{"asn": 65001, "routerID": "0.0.0.11", "export": map[string]any{"interfaces": []string{"node0"}}}),
		"BGPPeer": []any{
			doc("a", map[string]any{"asn": 65000, "address": "2001:db8::1", "interfaces": []string{"eth1"}, "password": nil, "bfd": true, "import": "default"}),
			doc("b", map[string]any{"asn": 65000, "address": "2001:db8::2", "interfaces": []string{"eth1"}, "password": nil, "bfd": true, "import": "default"}),
			doc("c", map[string]any{"asn": 65000, "address": nil, "interfaces": []string{"eth1", "eth0"}, "password": nil, "bfd": true, "import": "default"}),
		},
	}), frrFile)
	if want := "!\ninterface eth0\n ipv6 nd ra-lifetime 0\nexit\ninterface eth1\n ipv6 nd ra-lifetime 0\nexit\n!\nrouter bgp"; !strings.Contains(got, want) || strings.Count(got, "\ninterface eth1\n") != 1 {
		t.Errorf("frr.conf lacks:\n%s\nonce in:\n%s", want, got)
	}
}

// Without a BGPSpeaker the node runs no BGP; BGPPeers without one are an internal error.
func TestKubernetesFRRWithoutSpeaker(t *testing.T) {
	got := renderedFile(t, []string{"kubernetes-frr"}, kubernetesInput(t, map[string]any{}), frrFile)
	if !strings.HasSuffix(got, "service integrated-vtysh-config\n!\nend\n") || strings.Contains(got, "router bgp") {
		t.Errorf("frr.conf without a speaker:\n%s", got)
	}
	_, err := render(t, []string{"kubernetes-frr"}, kubernetesInput(t, map[string]any{
		"BGPPeer": []any{doc("fabric", map[string]any{"asn": 65000, "interfaces": []string{"eth0"}, "bfd": true, "import": "default"})},
	}))
	if err == nil || !strings.Contains(err.Error(), "internal error: the config holds BGPPeer documents but no BGPSpeaker") {
		t.Errorf("peers without a speaker: %v", err)
	}
}

// The renderer guards what frr.conf writes unquoted, which the schemas and rules refuse first.
func TestKubernetesFRRGuards(t *testing.T) {
	// peer is an unnumbered peer on eth0 with the fields given in place of its own.
	peer := func(fields map[string]any) map[string]any {
		m := map[string]any{"asn": 65000, "address": nil, "interfaces": []string{"eth0"}, "password": nil, "bfd": true, "import": "default"}
		for k, v := range fields {
			m[k] = v
		}
		return m
	}
	withRouterID := map[string]any{"asn": 65001, "routerID": "0.0.0.11", "export": map[string]any{"interfaces": []string{"eth0"}}}
	for name, c := range map[string]struct {
		speaker map[string]any
		peer    map[string]any
		name    string
		want    string
	}{
		"an interface with a space":      {withRouterID, peer(map[string]any{"interfaces": []string{"eth0 x"}}), "fabric", "is no interface name"},
		"a link-local address":           {withRouterID, peer(map[string]any{"address": "fe80::1", "interfaces": nil}), "fabric", "is no neighbour's address"},
		"an address on two interfaces":   {withRouterID, peer(map[string]any{"address": "2001:db8::1", "interfaces": []string{"eth0", "eth1"}}), "fabric", "binds one session to one"},
		"neither address nor interfaces": {withRouterID, peer(map[string]any{"interfaces": nil}), "fabric", "names neither an address nor interfaces"},
		"a peer of another name":         {withRouterID, peer(nil), "Fabric", "is no peer group's name"},
		"an import of other routes":      {withRouterID, peer(map[string]any{"import": "all"}), "fabric", "neither default nor none"},
		"a password of a bad key":        {withRouterID, peer(map[string]any{"password": map[string]any{"secretRef": map[string]any{"name": "bgp", "key": "a b"}}}), "fabric", "letters, digits"},
		"no router ID":                   {map[string]any{"asn": 65001, "routerID": nil, "export": map[string]any{"interfaces": []string{"eth0"}}}, peer(nil), "fabric", "gives no routerID"},
		"an export glob":                 {map[string]any{"asn": 65001, "routerID": "0.0.0.11", "export": map[string]any{"interfaces": []string{"eth*"}}}, peer(nil), "fabric", "is no interface name"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := render(t, []string{"kubernetes-frr"}, kubernetesInput(t, map[string]any{
				"BGPSpeaker": speaker(c.speaker),
				"BGPPeer":    []any{doc(c.name, c.peer)},
			}))
			if err == nil || !strings.Contains(err.Error(), "internal error") || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want an internal error with %q", err, c.want)
			}
		})
	}
}
