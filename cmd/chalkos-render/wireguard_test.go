package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/router/derive/derivetest"
)

// router-networkd renders a WireGuard's .netdev, whose keys name networkd's credentials, with a
// [WireGuardPeer] section per peer, and its .network with its addresses; a WireGuard of a VRF
// gets VRF= and one with an MTU gets MTUBytes=. The peers' allowed IPs are routed in the table of
// the WireGuard's VRF.
func TestRouterNetworkdWireGuard(t *testing.T) {
	const key = "YCfBku3dYhut+x0pFQMw7WbWU5qVks5ndJDuewg4J2I="
	got, err := unitFiles(t, []string{"router-networkd", networkFixture}, map[string][]any{
		"VRF": {derivetest.RouterDoc(t, "VRF", "tenant", `{"table": 100}`)},
		"WireGuard": {
			derivetest.RouterDoc(t, "WireGuard", "wg0", `{"listenPort": 51820, "addresses": ["fd00:f0::1/64"], "privateKey": {"secretRef": {"name": "wg", "key": "private"}}, "vrf": "default"}`),
			derivetest.RouterDoc(t, "WireGuard", "wg1", `{"listenPort": 51821, "addresses": ["fd00:f1::1/64", "192.0.2.1/24"], "mtu": 1380, "privateKey": {"secretRef": {"name": "wg", "key": "wg1"}}, "vrf": "tenant"}`),
		},
		"WireGuardPeer": {
			derivetest.RouterDoc(t, "WireGuardPeer", "laptop", `{"interface": "wg0", "publicKey": "`+key+`", "allowedIPs": ["fd00:f0::2/128"], "persistentKeepalive": 25, "presharedKey": {"secretRef": {"name": "wg", "key": "psk"}}}`),
			derivetest.RouterDoc(t, "WireGuardPeer", "office", `{"interface": "wg0", "publicKey": "TNcjWUC/YZFd6+6UkoYLORnFxvy/jvqzi7gh5Tc0lRU=", "allowedIPs": ["fd00:f0::3/128", "198.51.100.0/24"], "endpoint": "[2001:db8::1]:51820"}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"00-vrf-tenant.netdev": "[NetDev]\nKind=vrf\nName=tenant\n\n[VRF]\nTable=100\n\n",
		"00-vrf-tenant.network": "[Match]\nName=tenant\n\n[Link]\nRequiredForOnline=no\n\n" +
			"[Route]\nDestination=0.0.0.0/0\nMetric=4278198272\nTable=100\nType=unreachable\n\n" +
			"[Route]\nDestination=::/0\nMetric=4278198272\nTable=100\nType=unreachable\n\n",
		"00-wg-wg0.netdev": "[NetDev]\nKind=wireguard\nName=wg0\n\n[WireGuard]\nListenPort=51820\nPrivateKey=@secret_wg.private\nRouteTable=main\n\n" +
			"[WireGuardPeer]\nAllowedIPs=fd00:f0::2/128\nPersistentKeepalive=25\nPresharedKey=@secret_wg.psk\nPublicKey=" + key + "\n\n" +
			"[WireGuardPeer]\nAllowedIPs=fd00:f0::3/128\nAllowedIPs=198.51.100.0/24\nEndpoint=[2001:db8::1]:51820\nPublicKey=TNcjWUC/YZFd6+6UkoYLORnFxvy/jvqzi7gh5Tc0lRU=\n\n",
		"00-wg-wg0.network": "[Match]\nName=wg0\n\n[Network]\nAddress=fd00:f0::1/64\n\n",
		"00-wg-wg1.netdev":  "[NetDev]\nKind=wireguard\nName=wg1\n\n[WireGuard]\nListenPort=51821\nPrivateKey=@secret_wg.wg1\nRouteTable=100\n\n",
		"00-wg-wg1.network": "[Match]\nName=wg1\n\n[Link]\nMTUBytes=1380\n\n[Network]\nAddress=fd00:f1::1/64\nAddress=192.0.2.1/24\nVRF=tenant\n\n",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("units:\n%q\nwant:\n%q", got, want)
	}
}

// router-networkd refuses a NetDev of a WireGuard unit's name and one that creates the
// WireGuard's device, and the validator's refusals of the WireGuards.
func TestRouterNetworkdWireGuardRefusals(t *testing.T) {
	wg := derivetest.RouterDoc(t, "WireGuard", "wg0", `{"listenPort": 51820, "addresses": ["fd00:f0::1/64"], "privateKey": {"secretRef": {"name": "wg", "key": "private"}}, "vrf": "default"}`)
	for _, tc := range []struct {
		name string
		docs map[string][]any
		want string
	}{
		{"a NetDev of the unit's name", map[string][]any{"WireGuard": {wg}, "NetDev": {derivetest.Doc(t, "NetDev", "00-wg-wg0", `{"netDev": {"kind": "dummy", "name": "d0"}}`)}},
			"NetDev/00-wg-wg0: metadata.name: NetDev 00-wg-wg0 takes the name of WireGuard wg0's unit; rename the document"},
		{"a NetDev of the device", map[string][]any{"WireGuard": {wg}, "NetDev": {derivetest.Doc(t, "NetDev", "10-wg0", `{"netDev": {"kind": "dummy", "name": "wg0"}}`)}},
			"NetDev/10-wg0: spec.netDev.name: NetDev 10-wg0 creates the device wg0, the interface of WireGuard wg0; give the device another name"},
		{"a Network before the unit", map[string][]any{"WireGuard": {wg}, "Network": {derivetest.Doc(t, "Network", "00-a", `{"match": {"name": "wg*"}}`)}},
			"Network/00-a: metadata.name: Network 00-a matches the link of WireGuard wg0 before 00-wg-wg0; rename it to sort after 00-wg-wg0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := unitFiles(t, []string{"router-networkd", networkFixture}, tc.docs); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
