package router

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	routing "github.com/trevex/chalkos/pkg/router"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// chalkd router dhcp6-routes routes each recorded delegated prefix that a rendered DHCPv6
// config delegates on its link to the address that asked for it, in the link's VRF, until its
// lease ends; it removes the routes of delegated prefixes of no such record, leaves routes of
// other protocols alone, removes no record and reads no config of a file of no VRF's name.
func TestDHCP6Routes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	kernel := []*routing.KernelRoute{
		{Destination: "2001:db8:100::/56", Protocol: "dhcp-pd", Table: 500, NextHops: []*routing.NextHop{{Gateway: "fe80::1", Interface: "cust0"}}},
		{Destination: "2001:db8:100:100::/56", Protocol: "dhcp-pd", Table: 500, NextHops: []*routing.NextHop{{Gateway: "fe80::9", Interface: "cust0"}}},
		{Destination: "2001:db8:200::/48", Protocol: "static", Table: 254, NextHops: []*routing.NextHop{{Gateway: "fe80::1", Interface: "lan0"}}},
	}
	s, r := routerNode(t, kernel...)
	s.r.PDDir = t.TempDir()
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(s.r.KeaDir, "kea-dhcp6-access.json"), `{"Dhcp6": {"subnet6": [{"interface": "cust0", "pd-pools": [{"prefix": "2001:db8:100::", "prefix-len": 40, "delegated-len": 56}]}]}}`)
	write(filepath.Join(s.r.KeaDir, "kea-dhcp6.json"), `{"Dhcp6": {"subnet6": [{"interface": "lan1", "pd-pools": [{"prefix": "2001:db8:300::", "prefix-len": 48, "delegated-len": 64}]}]}}`)
	write(filepath.Join(s.r.KeaDir, "kea-dhcp6-Bad_Name.json"), `{"Dhcp6": {"subnet6": [{"interface": "bad0", "pd-pools": [{"prefix": "2001:db8:400::", "prefix-len": 48, "delegated-len": 64}]}]}}`)
	later := strconv.FormatInt(now.Unix()+600, 10)
	write(filepath.Join(s.r.PDDir, "cust0_2001:db8:100::_56"), "fe80::1 "+later+"\n")
	write(filepath.Join(s.r.PDDir, "lan1_2001:db8:300:1::_64"), "fe80::2 "+later+"\n")
	write(filepath.Join(s.r.PDDir, "bad0_2001:db8:400:1::_64"), "fe80::3 "+later+"\n")
	write(filepath.Join(s.r.PDDir, "cust0_2001:db8:100:200::_56"), "fe80::4 "+strconv.FormatInt(now.Unix()-1, 10)+"\n")
	if err := s.r.dhcp6Routes(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ip -6 route del 2001:db8:100:100::/56 table 500 proto 65",
		"ip -6 route replace 2001:db8:100::/56 via fe80::1 dev cust0 proto 65 vrf access expires 600",
		"ip -6 route replace 2001:db8:300:1::/64 via fe80::2 dev lan1 proto 65 expires 600",
	}
	if !slices.Equal(r.Calls, want) {
		t.Errorf("calls = %q, want %q", r.Calls, want)
	}
	left, _ := filepath.Glob(filepath.Join(s.r.PDDir, "*"))
	for i := range left {
		left[i] = filepath.Base(left[i])
	}
	if !slices.Equal(left, []string{"bad0_2001:db8:400:1::_64", "cust0_2001:db8:100:200::_56", "cust0_2001:db8:100::_56", "lan1_2001:db8:300:1::_64"}) {
		t.Errorf("records left = %v", left)
	}
}

// A route ip does not add, as on a link that is down, leaves the others routed and fails no run.
func TestDHCP6RoutesOfALinkDown(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s, r := routerNode(t)
	s.r.PDDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(s.r.KeaDir, "kea-dhcp6.json"), []byte(`{"Dhcp6": {"subnet6": [
		{"interface": "lan1", "pd-pools": [{"prefix": "2001:db8:300::", "prefix-len": 48, "delegated-len": 64}]},
		{"interface": "lan2", "pd-pools": [{"prefix": "2001:db8:400::", "prefix-len": 48, "delegated-len": 64}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	later := strconv.FormatInt(now.Unix()+600, 10)
	for name, via := range map[string]string{"lan1_2001:db8:300:1::_64": "fe80::2", "lan2_2001:db8:400:1::_64": "fe80::3"} {
		if err := os.WriteFile(filepath.Join(s.r.PDDir, name), []byte(via+" "+later+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r.Rules = append(r.Rules, tooltest.Rule{Prefix: "ip -6 route replace 2001:db8:300:1::/64", Err: errors.New("Network is down")})
	if err := s.r.dhcp6Routes(context.Background(), now); err != nil {
		t.Fatalf("a route of a link that is down failed the run: %v", err)
	}
	if !slices.Contains(r.Calls, "ip -6 route replace 2001:db8:400:1::/64 via fe80::3 dev lan2 proto 65 expires 600") {
		t.Errorf("the other link's prefix was not routed: %q", r.Calls)
	}
}

// Without the output kea, as in maintenance mode, a run removes no route: the kernel removes the
// routes when their leases end. With the output but no DHCPv6 config, as once a config drops
// the IPv6 subnets, it removes them.
func TestDHCP6RoutesWithoutConfigs(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	kernel := &routing.KernelRoute{Destination: "2001:db8:100::/56", Protocol: "dhcp-pd", Table: 500, NextHops: []*routing.NextHop{{Gateway: "fe80::1", Interface: "cust0"}}}
	for name, c := range map[string]struct {
		dir  func(t *testing.T) string
		want []string
	}{
		"no output":        {func(t *testing.T) string { return filepath.Join(t.TempDir(), "kea") }, nil},
		"no DHCPv6 config": {func(t *testing.T) string { return t.TempDir() }, []string{"ip -6 route del 2001:db8:100::/56 table 500 proto 65"}},
	} {
		t.Run(name, func(t *testing.T) {
			s, r := routerNode(t, kernel)
			s.r.KeaDir = c.dir(t)
			if err := s.r.dhcp6Routes(context.Background(), now); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(r.Calls, c.want) {
				t.Errorf("calls = %q, want %q", r.Calls, c.want)
			}
		})
	}
}

// A run removes the IPv6 routes of protocol dhcp-pd of no record, those of several next hops
// included, and leaves IPv4 routes of the protocol alone, which no delegated prefix makes.
func TestDHCP6RoutesRemoved(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s, r := routerNode(t,
		&routing.KernelRoute{Destination: "2001:db8:100::/56", Protocol: "dhcp-pd", Table: 500, NextHops: []*routing.NextHop{{Gateway: "fe80::1", Interface: "cust0", Weight: 1}, {Gateway: "fe80::2", Interface: "cust0", Weight: 1}}},
		&routing.KernelRoute{Destination: "198.51.100.0/24", Protocol: "dhcp-pd", Table: 254, NextHops: []*routing.NextHop{{Gateway: "192.0.2.2", Interface: "lan0"}}},
	)
	if err := os.WriteFile(filepath.Join(s.r.KeaDir, "kea-dhcp6.json"), []byte(`{"Dhcp6": {"subnet6": [{"interface": "cust0", "pd-pools": [{"prefix": "2001:db8:100::", "prefix-len": 40, "delegated-len": 56}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.r.PDDir, "cust0_2001:db8:100::_56"), []byte("fe80::1 "+strconv.FormatInt(now.Unix()+600, 10)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.r.dhcp6Routes(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ip -6 route del 2001:db8:100::/56 table 500 proto 65",
		"ip -6 route replace 2001:db8:100::/56 via fe80::1 dev cust0 proto 65 expires 600",
	}
	if !slices.Equal(r.Calls, want) {
		t.Errorf("calls = %q, want %q", r.Calls, want)
	}
}
