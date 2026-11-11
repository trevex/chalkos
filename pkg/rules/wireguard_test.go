package rules

import (
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/router/derive"
	"github.com/trevex/chalkos/pkg/schema"
)

// The WireGuard kinds' rules accept an interface with a peer, and refuse a WireGuard named past
// 15 characters, with the prefix of the NAT64s' links or as a VRF, and a private or preshared key
// whose Secret names no credential.
func TestWireGuardRules(t *testing.T) {
	set, doc := routerRules(t)
	tunnel := []config.Document{
		doc("WireGuard", "wg0", `{"addresses": ["fd00:f0::1/64"], "privateKey": {"secretRef": {"name": "wg", "key": "private"}}}`),
		doc("WireGuardPeer", "laptop", `{"interface": "wg0", "publicKey": "TNcjWUC/YZFd6+6UkoYLORnFxvy/jvqzi7gh5Tc0lRU=", "allowedIPs": ["fd00:f0::2/128"],
			"presharedKey": {"secretRef": {"name": "wg", "key": "laptop.psk"}}}`),
	}
	// with is the tunnel with the documents given, each in place of the tunnel's of its kind and name.
	with := func(docs ...config.Document) []config.Document {
		out := slices.Clone(tunnel)
		for _, w := range docs {
			if i := slices.IndexFunc(out, func(d config.Document) bool { return d.Kind == w.Kind && d.Name == w.Name }); i >= 0 {
				out[i] = w
			} else {
				out = append(out, w)
			}
		}
		return out
	}
	for _, tc := range []struct {
		name string
		docs []config.Document
		want []string
	}{
		{"an interface with a peer", tunnel, nil},
		{"a name past 15 characters", with(doc("WireGuard", "wg-of-the-office", `{"listenPort": 51821, "addresses": ["fd00:f1::1/64"], "privateKey": {"secretRef": {"name": "wg", "key": "office"}}}`)),
			[]string{"WireGuard wg-of-the-office has a name of more than 15 characters, which is no interface's name; give it a shorter one"}},
		{"a NAT64 link's name", with(doc("WireGuard", "n64-wg", `{"listenPort": 51821, "addresses": ["fd00:f1::1/64"], "privateKey": {"secretRef": {"name": "wg", "key": "office"}}}`)),
			[]string{"WireGuard n64-wg: n64- starts the names of the NAT64s' links; give the WireGuard another name"}},
		{"a VRF's name", with(doc("VRF", "wg0", `{"table": 100}`), doc("Router", "router", `{"management": {"interfaces": ["eth0"]}}`)),
			[]string{"WireGuard wg0 takes the name of a VRF, whose device has it; give the WireGuard another name"}},
		{"a private key whose Secret names no credential", with(doc("WireGuard", "wg0", `{"addresses": ["fd00:f0::1/64"], "privateKey": {"secretRef": {"name": "wg/keys", "key": "private"}}}`)),
			[]string{`WireGuard/wg0: spec.privateKey: the Secret's name and key of privateKey hold letters, digits, '.', '_' and '-' alone, as the names of systemd-networkd's credentials do`}},
		{"a preshared key whose Secret names no credential", with(doc("WireGuardPeer", "laptop", `{"interface": "wg0", "publicKey": "TNcjWUC/YZFd6+6UkoYLORnFxvy/jvqzi7gh5Tc0lRU=", "allowedIPs": ["fd00:f0::2/128"],
			"presharedKey": {"secretRef": {"name": "wg", "key": "laptop psk"}}}`)),
			[]string{`WireGuardPeer/laptop: spec.presharedKey: the Secret's name and key of presharedKey hold letters, digits, '.', '_' and '-' alone, as the names of systemd-networkd's credentials do`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failures, err := set.Evaluate(Input{Documents: config.Redact(tc.docs), Node: "r1"})
			if err != nil {
				t.Fatal(err)
			}
			if got := messages(failures); !slices.Equal(got, tc.want) {
				t.Errorf("failures:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

// The WireGuard kinds' schemas take public keys of 32 bytes in base64, endpoints of an IPv6
// address in brackets, an IPv4 address or a host name with a port from 1 to 65535, ports from 1
// and MTUs from 1280, and at least one address and range of allowed IPs.
func TestWireGuardSchemas(t *testing.T) {
	s, err := schema.LoadSchemas("../../test/fixtures/kinds/router")
	if err != nil {
		t.Fatal(err)
	}
	const key = `"privateKey": {"secretRef": {"name": "wg", "key": "private"}}`
	peer := func(fields string) string {
		return `{"interface": "wg0", "publicKey": "TNcjWUC/YZFd6+6UkoYLORnFxvy/jvqzi7gh5Tc0lRU=", "allowedIPs": ["fd00:f0::2/128"]` + fields + `}`
	}
	for _, tc := range []struct {
		kind, spec string
		ok         bool
	}{
		{"WireGuard", `{"addresses": ["fd00:f0::1/64", "192.0.2.1/24"], "mtu": 1280, ` + key + `}`, true},
		{"WireGuard", `{"addresses": [], ` + key + `}`, false},
		{"WireGuard", `{"addresses": ["fd00:f0::1"], ` + key + `}`, false},
		{"WireGuard", `{"addresses": ["fd00:f0::1/64"], "listenPort": 0, ` + key + `}`, false},
		{"WireGuard", `{"addresses": ["fd00:f0::1/64"], "mtu": 1279, ` + key + `}`, false},
		{"WireGuard", `{"addresses": ["fd00:f0::1/64"]}`, false},
		{"WireGuardPeer", peer(``), true},
		{"WireGuardPeer", peer(`, "endpoint": "[2001:db8::1]:51820", "persistentKeepalive": 25`), true},
		{"WireGuardPeer", peer(`, "endpoint": "192.0.2.1:65535"`), true},
		{"WireGuardPeer", peer(`, "endpoint": "vpn.example.org:1"`), true},
		{"WireGuardPeer", peer(`, "endpoint": "2001:db8::1:51820"`), false},
		{"WireGuardPeer", peer(`, "endpoint": "vpn.example.org:65536"`), false},
		{"WireGuardPeer", peer(`, "endpoint": "vpn.example.org"`), false},
		{"WireGuardPeer", peer(`, "persistentKeepalive": 0`), false},
		{"WireGuardPeer", `{"interface": "wg0", "publicKey": "TNcjWUC/YZFd6+6UkoYLORnFxvy/jvqzi7gh5Tc0lRU", "allowedIPs": ["fd00:f0::2/128"]}`, false},
		{"WireGuardPeer", `{"interface": "wg0", "publicKey": "TNcjWUC/YZFd6+6UkoYLORnFxvy/jvqzi7gh5Tc0lRV=", "allowedIPs": ["fd00:f0::2/128"]}`, false},
		{"WireGuardPeer", `{"interface": "wg0", "publicKey": "TNcjWUC/YZFd6+6UkoYLORnFxvy/jvqzi7gh5Tc0lRU=", "allowedIPs": []}`, false},
	} {
		d := builtinDoc(t, derive.API, tc.kind, "a", tc.spec)
		if err := s.Validate(d.Object); (err == nil) != tc.ok {
			t.Errorf("%s %s: err = %v, want accepted %v", tc.kind, tc.spec, err, tc.ok)
		}
	}
}
