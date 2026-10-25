package router

import (
	"strings"
	"testing"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
)

// A filter keeps the routes within its prefix, of its protocol and of its table, main unless
// given; BGP's paths go with the protocol bgp or none.
func TestFilter(t *testing.T) {
	kernel := []*nodev1.KernelRoute{
		{Destination: "::/0", Protocol: "bgp", Table: 254},
		{Destination: "2001:db8:1::/64", Protocol: "kernel", Table: 254},
		{Destination: "2001:db8:1::1/128", Protocol: "kernel", Table: 255},
		{Destination: "192.0.2.0/24", Protocol: "static", Table: 254},
		{Destination: "198.51.100.0/24", Protocol: "bgp", Table: 100},
	}
	bgp := []*nodev1.BGPRoute{{Prefix: "::/0"}, {Prefix: "2001:db8:1::/48"}, {Prefix: "192.0.2.0/24"}}
	for _, c := range []struct {
		prefix, protocol, table string
		kernel, bgp             []string
	}{
		{"", "", "", []string{"::/0", "2001:db8:1::/64", "192.0.2.0/24"}, []string{"::/0", "2001:db8:1::/48", "192.0.2.0/24"}},
		{"2001:db8::/32", "", "", []string{"2001:db8:1::/64"}, []string{"2001:db8:1::/48"}},
		{"::/0", "", "", []string{"::/0", "2001:db8:1::/64"}, []string{"::/0", "2001:db8:1::/48"}},
		{"0.0.0.0/0", "bgp", "all", []string{"198.51.100.0/24"}, []string{"192.0.2.0/24"}},
		{"", "kernel", "local", []string{"2001:db8:1::1/128"}, nil},
		{"", "static", "254", []string{"192.0.2.0/24"}, nil},
		// A protocol's number stands for its name.
		{"", "4", "", []string{"192.0.2.0/24"}, nil},
		{"0.0.0.0/0", "186", "all", []string{"198.51.100.0/24"}, []string{"192.0.2.0/24"}},
		{"192.0.2.1/32", "", "", nil, nil},
	} {
		f, err := NewFilter(c.prefix, c.protocol, c.table)
		if err != nil {
			t.Fatal(err)
		}
		var gotKernel, gotBGP []string
		for _, r := range f.Kernel(kernel) {
			gotKernel = append(gotKernel, r.Destination)
		}
		if f.WantsBGP() {
			for _, r := range f.BGP(bgp) {
				gotBGP = append(gotBGP, r.Prefix)
			}
		}
		if strings.Join(gotKernel, " ") != strings.Join(c.kernel, " ") || strings.Join(gotBGP, " ") != strings.Join(c.bgp, " ") {
			t.Errorf("%q %q %q: kernel %v, bgp %v; want %v, %v", c.prefix, c.protocol, c.table, gotKernel, gotBGP, c.kernel, c.bgp)
		}
	}
}

// A filter refuses what no route has.
func TestFilterRefusals(t *testing.T) {
	for _, c := range []struct{ prefix, protocol, table, want string }{
		{"2001:db8::", "", "", `prefix "2001:db8::" is no address range`},
		{"", "frr", "", `protocol "frr" is none the kernel names`},
		{"", "", "mine", `table "mine" is none of main, local, default and all`},
	} {
		if _, err := NewFilter(c.prefix, c.protocol, c.table); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c, err)
		}
	}
}
