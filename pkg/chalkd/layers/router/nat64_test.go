package router

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/internal/testwait"
	routing "github.com/trevex/chalkos/pkg/router"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// kernel is a fake kernel's links, IPv4 addresses and protocol-64 routes.
type kernel struct {
	links  []routing.Link
	addrs  []routing.Addr
	routes map[int][]routing.TableRoute
}

// nat64Node is the router layer of a node whose kernel is k, with the output jool in a directory of
// its own and the named namespaces in another.
func nat64Node(t *testing.T, k *kernel) (*Plugin, *tooltest.Runner) {
	t.Helper()
	p, r := routerNode(t)
	p.Links = func() ([]routing.Link, error) { return k.links, nil }
	p.IPv4Addrs = func(link string) ([]routing.Addr, error) {
		var out []routing.Addr
		for _, a := range k.addrs {
			if link == "" || a.Link == link {
				out = append(out, a)
			}
		}
		return out, nil
	}
	p.ProtocolRoutes = func(family, proto int) ([]routing.TableRoute, error) {
		if proto != nat64Protocol {
			t.Errorf("routes of protocol %d asked for", proto)
		}
		return k.routes[family], nil
	}
	p.NetnsDir = t.TempDir()
	// Each namespace's peer is missing until a rule says otherwise, and has no addresses.
	r.Rules = append(r.Rules, tooltest.Rule{Prefix: "nsenter --net=" + p.NetnsDir + "/nat64-edge ip link show dev nat64", Err: errors.New("Device \"nat64\" does not exist.")})
	return p, r
}

// edge renders the NAT64 edge of the default VRF, with prefix 64:ff9b::/96 and a pool of
// 192.0.2.252/30, and returns its Jool file.
func edge(t *testing.T, p *Plugin, veth string) string {
	t.Helper()
	f := filepath.Join(p.NAT64Dir, "edge.json")
	touch(t, f, `{"instance": "edge", "framework": "netfilter", "global": {"pool6": "64:ff9b::/96"}}`)
	touch(t, filepath.Join(p.NAT64Dir, "edge.veth"), veth)
	return f
}

const edgeVeth = `{"table": 254, "vrf": null, "outer": "100.64.0.1/30", "inner": "100.64.0.2/30", "routes": ["192.0.2.252/30"]}`

// A first run makes the namespace, link, instance and routes of a NAT64, and removes the routes,
// links and namespaces of NAT64s that went.
func TestNAT64(t *testing.T) {
	k := &kernel{
		links: []routing.Link{{Name: "eth0"}, {Name: "n64-old", Kind: "veth"}, {Name: "n64-dummy", Kind: "dummy"}},
		addrs: []routing.Addr{{Link: "eth0", Prefix: netip.MustParsePrefix("203.0.113.1/24")}},
		routes: map[int][]routing.TableRoute{
			netlink.FAMILY_V4: {{Table: 254, Destination: netip.MustParsePrefix("192.0.2.252/30")}, {Table: 300, Destination: netip.MustParsePrefix("198.51.100.9/32")}},
			netlink.FAMILY_V6: {{Table: 254, Destination: netip.MustParsePrefix("64:ff9b::/96")}, {Table: 254, Destination: netip.MustParsePrefix("2001:db8:64::/96")}},
		},
	}
	p, r := nat64Node(t, k)
	file := edge(t, p, edgeVeth)
	touch(t, filepath.Join(p.NetnsDir, "nat64-old"), "")
	ns := "nsenter --net=" + p.NetnsDir + "/nat64-edge "
	r.Rules = append(r.Rules, tooltest.Rule{Prefix: ns + "ip -4 -j addr show dev nat64", Out: `[{"ifname": "nat64", "addr_info": [{"local": "100.64.0.6", "prefixlen": 30}]}]`})
	var stderr bytes.Buffer
	if err := p.reconcileNAT64(context.Background(), false, &stderr); err != nil {
		t.Fatalf("%v: %s", err, stderr.String())
	}
	want := []string{
		"jool file check " + file,
		"ip netns add nat64-edge",
		ns + "sysctl -q -w net.ipv4.conf.all.forwarding=0 net.ipv4.conf.default.forwarding=0 net.ipv6.conf.all.forwarding=0 net.ipv6.conf.default.forwarding=0",
		ns + "ip link show dev nat64",
		"ip link add n64-edge mtu 65535 type veth peer name nat64 mtu 65535 netns nat64-edge",
		"ip link set dev n64-edge nomaster",
		"ip addr replace 100.64.0.1/30 dev n64-edge",
		"ip -6 addr replace fe80::1/64 dev n64-edge nodad",
		"ip link set dev n64-edge up",
		ns + "ip link set dev lo up",
		ns + "ip -4 -j addr show dev nat64",
		ns + "ip addr del 100.64.0.6/30 dev nat64",
		ns + "ip addr replace 100.64.0.2/30 dev nat64",
		ns + "ip -6 addr replace fe80::2/64 dev nat64 nodad",
		ns + "ip link set dev nat64 up",
		ns + "ip route replace default via 100.64.0.1 dev nat64",
		ns + "ip -6 route replace default via fe80::1 dev nat64",
		ns + "jool -i edge global display --csv",
		ns + "jool file handle " + file,
		"ip -6 route replace 64:ff9b::/96 via fe80::2 dev n64-edge table 254 proto 64",
		"ip -4 route replace 192.0.2.252/30 via 100.64.0.2 dev n64-edge table 254 proto 64",
		"ip -4 route del 198.51.100.9/32 table 300 proto 64",
		"ip -6 route del 2001:db8:64::/96 table 254 proto 64",
		"ip link del n64-old",
		"ip netns delete nat64-old",
	}
	if !slices.Equal(r.Calls, want) {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(r.Calls, "\n"), strings.Join(want, "\n"))
	}
}

// A second run over a namespace and link that exist keeps both, sets the namespace's forwarding
// off again and replaces the instance in place; a new pool6 removes the instance first.
func TestNAT64Again(t *testing.T) {
	for name, c := range map[string]struct {
		current string
		remove  bool
	}{
		"same pool6":  {"pool6,64:ff9b::/96\n", false},
		"new pool6":   {"pool6,2001:db8:64::/96\n", true},
		"no instance": {"", false},
	} {
		t.Run(name, func(t *testing.T) {
			k := &kernel{links: []routing.Link{{Name: "n64-edge", Kind: "veth"}}, addrs: []routing.Addr{{Link: "n64-edge", Prefix: netip.MustParsePrefix("100.64.0.1/30")}}}
			p, r := nat64Node(t, k)
			file := edge(t, p, edgeVeth)
			touch(t, filepath.Join(p.NetnsDir, "nat64-edge"), "")
			ns := "nsenter --net=" + p.NetnsDir + "/nat64-edge "
			r.Rules = append([]tooltest.Rule{
				{Prefix: ns + "ip link show dev nat64"},
				{Prefix: ns + "ip -4 -j addr show dev nat64", Out: `[{"addr_info": [{"local": "100.64.0.2", "prefixlen": 30}]}]`},
				{Prefix: ns + "jool -i edge global display --csv", Out: c.current},
			}, r.Rules...)
			var stderr bytes.Buffer
			if err := p.reconcileNAT64(context.Background(), false, &stderr); err != nil {
				t.Fatalf("%v: %s", err, stderr.String())
			}
			for _, call := range r.Calls {
				if strings.HasPrefix(call, "ip netns add") || strings.HasPrefix(call, "ip link add") || strings.Contains(call, "addr del") || strings.Contains(call, " del ") {
					t.Errorf("a run over a namespace and link that exist ran %q", call)
				}
			}
			removed := slices.Index(r.Calls, ns+"jool -i edge instance remove")
			handled := slices.Index(r.Calls, ns+"jool file handle "+file)
			if (removed >= 0) != c.remove || handled < 0 || removed > handled {
				t.Errorf("instance removed at %d and handled at %d of %q", removed, handled, r.Calls)
			}
		})
	}
}

// A file jool or the link's form refuses, or a pool that holds an address of a router link,
// changes nothing.
func TestNAT64Refused(t *testing.T) {
	for name, c := range map[string]struct {
		veth, jool, says string
	}{
		"jool refuses":        {edgeVeth, "jool file check", "jool refuses"},
		"a table of a string": {`{"table": "254", "vrf": null, "outer": "100.64.0.1/30", "inner": "100.64.0.2/30", "routes": []}`, "", "is no link's file"},
		"a VRF of a slash":    {`{"table": 254, "vrf": "a/b", "outer": "100.64.0.1/30", "inner": "100.64.0.2/30", "routes": []}`, "", "is no link's file"},
		"a route of IPv6":     {`{"table": 254, "outer": "100.64.0.1/30", "inner": "100.64.0.2/30", "routes": ["2001:db8::/64"]}`, "", "is no link's file"},
		"no inner address":    {`{"table": 254, "outer": "100.64.0.1/30", "routes": []}`, "", "is no link's file"},
		"no object":           {`[]`, "", "is no link's file"},
		"a link's address":    {edgeVeth, "", "NAT64 edge: the pool entry 192.0.2.252/30 holds 192.0.2.253, an address of the link eth4, which the router answers itself"},
	} {
		t.Run(name, func(t *testing.T) {
			k := &kernel{addrs: []routing.Addr{{Link: "eth0", Prefix: netip.MustParsePrefix("203.0.113.1/24")}, {Link: "eth4", Prefix: netip.MustParsePrefix("192.0.2.253/24")}}}
			p, r := nat64Node(t, k)
			file := edge(t, p, c.veth)
			if c.jool != "" {
				r.Rules = append([]tooltest.Rule{{Prefix: c.jool, Err: errors.New("exit status 1")}}, r.Rules...)
			}
			var stderr bytes.Buffer
			err := p.reconcileNAT64(context.Background(), false, &stderr)
			if err != cli.Status(1) || !strings.Contains(stderr.String(), c.says) || !strings.Contains(stderr.String(), "no instance changed") {
				t.Errorf("error %v, said %q", err, stderr.String())
			}
			if want := []string{"jool file check " + file}; !slices.Equal(r.Calls, want) {
				t.Errorf("calls = %q, want %q", r.Calls, want)
			}
		})
	}
}

// NAT64s of a VRF that lacks the table of their files wait 30 seconds once, fail and keep their
// routes, while the other NAT64s run.
func TestNAT64VRFWithoutTable(t *testing.T) {
	testwait.Bubble(t, func(t *testing.T) {
		k := &kernel{
			links:  []routing.Link{{Name: "guest", Kind: "vrf", Table: 301}},
			routes: map[int][]routing.TableRoute{netlink.FAMILY_V6: {{Table: 300, Destination: netip.MustParsePrefix("64:ff9b::/96")}}},
		}
		p, r := nat64Node(t, k)
		guest := `{"table": 300, "vrf": "guest", "outer": "100.64.0.1/30", "inner": "100.64.0.2/30", "routes": []}`
		for _, name := range []string{"a", "b"} {
			touch(t, filepath.Join(p.NAT64Dir, name+".json"), `{"global": {"pool6": "64:ff9b::/96"}}`)
			touch(t, filepath.Join(p.NAT64Dir, name+".veth"), guest)
		}
		start := time.Now()
		var stderr bytes.Buffer
		if err := p.reconcileNAT64(context.Background(), false, &stderr); err != cli.Status(1) {
			t.Errorf("error %v", err)
		}
		if took := time.Since(start); took < vrfTableWait || took > vrfTableWait+time.Second {
			t.Errorf("waited %v", took)
		}
		for _, says := range []string{
			"VRF guest has no table 300 after 30 s",
			"failed to bring up the namespace and link of a",
			"failed to bring up the namespace and link of b",
		} {
			if !strings.Contains(stderr.String(), says) {
				t.Errorf("did not say %q: %s", says, stderr.String())
			}
		}
		for _, call := range r.Calls {
			if strings.Contains(call, "route") || strings.Contains(call, "master") || strings.Contains(call, "jool file handle") {
				t.Errorf("ran %q", call)
			}
		}
	})
}

// A device of another kind of a NAT64 link's name fails the NAT64 and stays.
func TestNAT64DeviceOfAnotherKind(t *testing.T) {
	p, r := nat64Node(t, &kernel{links: []routing.Link{{Name: "n64-edge", Kind: "dummy"}}})
	edge(t, p, edgeVeth)
	var stderr bytes.Buffer
	if err := p.reconcileNAT64(context.Background(), false, &stderr); err != cli.Status(1) {
		t.Errorf("error %v", err)
	}
	if !strings.Contains(stderr.String(), "n64-edge is a device of kind dummy, not a NAT64's link") || !strings.Contains(stderr.String(), "failed to bring up the namespace and link of edge") {
		t.Errorf("said %q", stderr.String())
	}
	for _, call := range r.Calls {
		if strings.HasPrefix(call, "ip link del") || strings.HasPrefix(call, "ip link add") {
			t.Errorf("ran %q", call)
		}
	}
}

// stop, and a missing output directory, remove every NAT64's routes, links and namespaces, and
// a failed step does not stop the others.
func TestNAT64Stop(t *testing.T) {
	for name, stop := range map[string]bool{"stop": true, "no output": false} {
		t.Run(name, func(t *testing.T) {
			k := &kernel{
				links:  []routing.Link{{Name: "n64-edge", Kind: "veth"}, {Name: "n64-guest", Kind: "veth"}, {Name: "eth0"}},
				routes: map[int][]routing.TableRoute{netlink.FAMILY_V6: {{Table: 254, Destination: netip.MustParsePrefix("64:ff9b::/96")}}},
			}
			p, r := nat64Node(t, k)
			if stop {
				edge(t, p, edgeVeth)
			} else {
				p.NAT64Dir = filepath.Join(t.TempDir(), "jool")
			}
			touch(t, filepath.Join(p.NetnsDir, "nat64-edge"), "")
			r.Rules = append(r.Rules, tooltest.Rule{Prefix: "ip link del n64-edge", Err: errors.New("busy")})
			var stderr bytes.Buffer
			if err := p.reconcileNAT64(context.Background(), stop, &stderr); err != cli.Status(1) {
				t.Errorf("error %v", err)
			}
			want := []string{
				"ip -6 route del 64:ff9b::/96 table 254 proto 64",
				"ip link del n64-edge",
				"ip link del n64-guest",
				"ip netns delete nat64-edge",
			}
			if !slices.Equal(r.Calls, want) {
				t.Errorf("calls = %q, want %q", r.Calls, want)
			}
			if got := stderr.String(); got != "chalkd router nat64: failed to remove the link n64-edge\n" {
				t.Errorf("said %q", got)
			}
		})
	}
}
