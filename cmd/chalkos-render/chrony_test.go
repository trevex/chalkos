package main

import (
	"reflect"
	"testing"

	"github.com/trevex/chalkos/pkg/router/derive"
	"github.com/trevex/chalkos/pkg/router/derive/derivetest"
)

// A TimeServer renders an allow line per masked client range, a rate limit per client and, with localStratum, the local
// reference in orphan mode; a node without one gets no file.
func TestChronyServer(t *testing.T) {
	timeServer := func(spec string) map[string][]any {
		return map[string][]any{"TimeServer": {derivetest.RouterDoc(t, "TimeServer", "ntp", spec)}}
	}
	got := renderedFile(t, []string{"chrony-server"}, inputOf(t, timeServer(`{"listen": ["2001:db8:1::1", "192.0.2.1"], "allow": ["2001:db8:1::1/64", "192.0.2.0/24"], "localStratum": 10}`)), chronyServerFile)
	want := "# The node's TimeServer, rendered by chalkos-render chrony-server.\n" +
		"allow 2001:db8:1::/64\n" +
		"allow 192.0.2.0/24\n" +
		"ratelimit interval 1 burst 16\n" +
		"local stratum 10 orphan\n"
	if got != want {
		t.Errorf("server.conf =\n%s\nwant\n%s", got, want)
	}
	got = renderedFile(t, []string{"chrony-server"}, inputOf(t, timeServer(`{"listen": ["2001:db8:1::1"], "allow": ["2001:db8:1::/64"], "localStratum": null}`)), chronyServerFile)
	if want := "# The node's TimeServer, rendered by chalkos-render chrony-server.\nallow 2001:db8:1::/64\nratelimit interval 1 burst 16\n"; got != want {
		t.Errorf("server.conf without localStratum =\n%s\nwant\n%s", got, want)
	}
	if got := renderedFiles(t, []string{"chrony-server"}, inputOf(t, map[string][]any{"TimeServer": {}})); got != "" {
		t.Errorf("files without a TimeServer = %q", got)
	}
	for spec, want := range map[string]string{
		`{"listen": ["2001:db8:1::1"], "allow": [], "localStratum": null}`:                "internal error: TimeServer ntp: allow names no range, so the firewall would accept NTP from every client",
		`{"listen": ["2001:db8:1::1"], "allow": ["2001:db8:1::/64"], "localStratum": 16}`: "internal error: TimeServer ntp: the local stratum 16 is outside 1 to 15",
	} {
		refused(t, []string{"chrony-server"}, timeServer(spec), want)
	}
	two := timeServer(`{"listen": ["2001:db8:1::1"], "allow": ["2001:db8:1::/64"], "localStratum": null}`)
	two["TimeServer"] = append(two["TimeServer"], derivetest.RouterDoc(t, "TimeServer", "other", `{"listen": ["2001:db8:1::1"], "allow": ["2001:db8:1::/64"], "localStratum": null}`))
	refused(t, []string{"chrony-server"}, two, "internal error: the input holds 2 TimeServer documents; a node has one")
}

// A TimeServer opens NTP over UDP from its clients' ranges to the addresses it listens on, in the
// default VRF. It guards an empty listen or allow, which would open the port wider.
func TestTimeServerOpenings(t *testing.T) {
	got, err := timeServerOpenings([]derive.Named[derive.TimeServerSpec]{
		{Name: "ntp", Spec: derive.TimeServerSpec{Listen: addrs("2001:db8:1::1", "192.0.2.1"), Allow: ranges("2001:db8:1::1/64", "192.0.2.0/24")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []opening{{comment: "TimeServer ntp", protocol: "udp", ports: []derive.PortRange{"123"}, sources: ranges("2001:db8:1::/64", "192.0.2.0/24"), destinations: ranges("2001:db8:1::1/128", "192.0.2.1/32")}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("timeServerOpenings =\n%+v\nwant\n%+v", got, want)
	}
	if lines := got[0].lines(); !reflect.DeepEqual(lines, []string{
		`ip saddr { 192.0.2.0/24 } ip daddr { 192.0.2.1/32 } udp dport { 123 } accept comment "TimeServer ntp"`,
		`ip6 saddr { 2001:db8:1::/64 } ip6 daddr { 2001:db8:1::1/128 } udp dport { 123 } accept comment "TimeServer ntp"`,
	}) {
		t.Errorf("lines = %q", lines)
	}
	if _, err := timeServerOpenings([]derive.Named[derive.TimeServerSpec]{{Name: "ntp", Spec: derive.TimeServerSpec{Allow: ranges("2001:db8:1::/64")}}}); err == nil ||
		err.Error() != "internal error: TimeServer ntp: listen names no address, so the firewall would accept NTP on every address of the router; chalkd checks the config against the schemas, the rules and the validators before it renders" {
		t.Errorf("err = %v", err)
	}
}
