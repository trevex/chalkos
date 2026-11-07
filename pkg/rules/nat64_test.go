package rules

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/router/derive"
	"github.com/trevex/chalkos/pkg/schema"
)

// manyRecords are records of a Resolver's local zones, many more than a router answers for, which
// the rules check within chalkd's runtime cost limit.
const manyRecords = 2000

// The NAT64 and Resolver rules accept a fabric edge's documents, NAT64s of empty pools, one prefix
// in two VRFs, resolvers of two VRFs and many records, and refuse a name whose link n64-<name> is
// too long, prefixes no NAT64 or DNS64 takes, a pool entry of IPv6, of too many addresses, of
// addresses no connection leaves from, of link-local addresses or of the kernel's ephemeral
// ports, two entries of one protocol's addresses and ports, addresses no resolver listens on or
// forwards to, local zones with records outside them or over ipv4only.arpa with DNS64, records
// whose data is not of their type, and names past 253 characters. Two NAT64s of one prefix or of
// one pool's addresses, a record twice and a CNAME beside another record pass them: the outputs
// jool and unbound compare those in pairs.
func TestNAT64Rules(t *testing.T) {
	set, doc := routerRules(t)
	edge := []config.Document{
		doc("NAT64", "edge", `{"pool4": [{"prefix": "192.0.2.8/30"}, {"prefix": "198.51.100.1/32", "ports": "1-32767", "protocols": ["tcp"]}]}`),
		doc("NAT64", "lab", `{"prefix": "2001:db8:64::/96", "pool4": [{"prefix": "203.0.113.1/32", "ports": 61001}, {"prefix": "203.0.113.0/30", "ports": "61002-65535"}]}`),
		doc("Resolver", "resolver", `{"listen": ["2001:db8:ffff::1", "192.0.2.1"], "allow": ["2001:db8::/32"], "forwarders": ["2001:db8::53"],
			"dns64": {}, "localZones": [{"name": "lan.example.org", "records": [{"name": "r1.lan.example.org", "type": "AAAA", "value": "2001:db8::1"},
			{"name": "r1.lan.example.org", "type": "A", "value": "192.0.2.1"}, {"name": "lan.example.org", "type": "MX", "value": "10 r1.lan.example.org"},
			{"name": "_imaps._tcp.lan.example.org", "type": "SRV", "value": "0 1 993 r1.lan.example.org"}, {"name": "www.lan.example.org", "type": "CNAME", "value": "r1.lan.example.org"},
			{"name": "lan.example.org", "type": "TXT", "value": "v=spf1 -all"}]}, {"name": "2.0.192.in-addr.arpa", "records": [{"name": "1.2.0.192.in-addr.arpa", "type": "PTR", "value": "r1.lan.example.org"}]}]}`),
	}
	// resolver is the resolver of the fabric edge with the local zone lan.example.org of the
	// records given, in JSON.
	resolver := func(records string) config.Document {
		return doc("Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32"], "localZones": [{"name": "lan.example.org", "records": [`+records+`]}]}`)
	}
	// record is a record of a.lan.example.org of the type and value given.
	record := func(typ, value string) string {
		return `{"name": "a.lan.example.org", "type": "` + typ + `", "value": ` + strconv.Quote(value) + `}`
	}
	recordMessage := func(typ, value, is string) []string {
		return []string{`Resolver/resolver: spec.localZones: the ` + typ + ` record of a.lan.example.org holds ` + value + `, which is no ` + is}
	}
	long := strings.Repeat(strings.Repeat("a", 63)+".", 3) + strings.Repeat("a", 46) + ".lan.example.org"
	// many are n records of one name, AAAA records or TXT records of 254 characters, which the
	// rules check at the highest cost per record.
	many := func(n int, typ string) string {
		var records []string
		for i := range n {
			value := "2001:db8::" + strconv.FormatInt(int64(i), 16)
			if typ == "TXT" {
				value = strings.Repeat("x", 250) + strconv.Itoa(1000+i)
			}
			records = append(records, `{"name": "h.lan.example.org", "type": "`+typ+`", "value": "`+value+`"}`)
		}
		return strings.Join(records, ", ")
	}
	replace := func(with ...config.Document) []config.Document {
		out := slices.Clone(edge)
		for _, w := range with {
			i := slices.IndexFunc(out, func(d config.Document) bool { return d.Kind == w.Kind && d.Name == w.Name })
			if i < 0 {
				out = append(out, w)
			} else {
				out[i] = w
			}
		}
		return out
	}
	// tenant is a VRF of eth1, with the Network documents that leave eth0 in the default VRF.
	tenant := []config.Document{network(t, "10-eth0", `{"name": "eth0"}`), network(t, "20-eth1", `{"name": "eth1"}`), doc("VRF", "tenant", `{"table": 100, "interfaces": ["eth1"]}`)}
	for _, tc := range []struct {
		name string
		docs []config.Document
		want []string
	}{
		{"a fabric edge", edge, nil},
		{"a name of 12 characters", replace(doc("NAT64", "abcdefghijkl", `{"prefix": "2001:db8:65::/96"}`)),
			[]string{"NAT64 abcdefghijkl has a name of more than 11 characters, which its link n64-<name> does not take as an interface's name; give it a shorter one"}},
		{"a name of 11 characters", replace(doc("NAT64", "abcdefghijk", `{"prefix": "2001:db8:65::/96"}`)), nil},
		{"a prefix of 95 bits", replace(doc("NAT64", "lab", `{"prefix": "2001:db8:64::/95"}`)),
			[]string{`NAT64/lab: spec.prefix: the prefix 2001:db8:64::/95 is no NAT64 prefix; give an IPv6 range of 32, 40, 48, 56, 64 or 96 bits, such as 64:ff9b::/96`}},
		{"an IPv4 prefix", replace(doc("NAT64", "lab", `{"prefix": "192.0.2.0/24"}`)),
			[]string{`NAT64/lab: spec.prefix: the prefix 192.0.2.0/24 is no NAT64 prefix; give an IPv6 range of 32, 40, 48, 56, 64 or 96 bits, such as 64:ff9b::/96`}},
		// The output jool compares the NAT64s' prefixes and pools in pairs.
		{"two NAT64s of one prefix", replace(doc("NAT64", "lab", `{"prefix": "64:ff9b::/96"}`)), nil},
		{"one prefix in two VRFs", replace(slices.Concat(tenant, []config.Document{doc("NAT64", "lab", `{"vrf": "tenant", "prefix": "64:ff9b::/96"}`)})...), nil},
		{"a link-local pool", replace(doc("NAT64", "lab", `{"prefix": "2001:db8:64::/96", "pool4": [{"prefix": "169.254.64.0/30"}]}`)),
			[]string{`NAT64/lab: spec.pool4: the pool entry 169.254.64.0/30 holds link-local addresses, which the NAT64s' links take`}},
		{"resolvers of two VRFs", replace(slices.Concat(tenant, []config.Document{doc("Resolver", "tenant", `{"vrf": "tenant", "listen": ["2001:db8:1::1"], "allow": ["2001:db8:1::/64"]}`)})...), nil},
		{"two NAT64s of one pool's addresses", replace(doc("NAT64", "lab", `{"prefix": "2001:db8:64::/96", "pool4": [{"prefix": "192.0.2.9/32", "ports": "1-32767"}]}`)), nil},
		{"an IPv6 pool", replace(doc("NAT64", "lab", `{"prefix": "2001:db8:64::/96", "pool4": [{"prefix": "2001:db8::/64"}]}`)),
			[]string{`NAT64/lab: spec.pool4: the pool entry 2001:db8::/64 is no IPv4 range of 16 bits or more`}},
		{"a pool of a /15", replace(doc("NAT64", "lab", `{"prefix": "2001:db8:64::/96", "pool4": [{"prefix": "198.18.0.0/15"}]}`)),
			[]string{`NAT64/lab: spec.pool4: the pool entry 198.18.0.0/15 is no IPv4 range of 16 bits or more`}},
		{"the kernel's ephemeral ports", replace(doc("NAT64", "lab", `{"prefix": "2001:db8:64::/96", "pool4": [{"prefix": "203.0.113.1/32", "ports": "1-65535"}]}`)),
			[]string{`NAT64/lab: spec.pool4: a pool entry takes the ports 1-65535, which touch the kernel's ephemeral ports, 32768 to 60999, which Jool refuses; give ports from 1 to 32767 or from 61000 to 65535`}},
		{"one ephemeral port", replace(doc("NAT64", "lab", `{"prefix": "2001:db8:64::/96", "pool4": [{"prefix": "203.0.113.1/32", "ports": 40000}]}`)),
			[]string{`NAT64/lab: spec.pool4: a pool entry takes the ports 40000, which touch the kernel's ephemeral ports, 32768 to 60999, which Jool refuses; give ports from 1 to 32767 or from 61000 to 65535`}},
		{"the last port below the ephemeral ones", replace(doc("NAT64", "lab", `{"prefix": "2001:db8:64::/96", "pool4": [{"prefix": "203.0.113.1/32", "ports": "1-32767"}]}`)), nil},
		{"the first port above the ephemeral ones", replace(doc("NAT64", "lab", `{"prefix": "2001:db8:64::/96", "pool4": [{"prefix": "203.0.113.1/32", "ports": "61000-65535"}]}`)), nil},
		{"a range up to the first ephemeral port", replace(doc("NAT64", "lab", `{"prefix": "2001:db8:64::/96", "pool4": [{"prefix": "203.0.113.1/32", "ports": "30000-32768"}]}`)),
			[]string{`NAT64/lab: spec.pool4: a pool entry takes the ports 30000-32768, which touch the kernel's ephemeral ports, 32768 to 60999, which Jool refuses; give ports from 1 to 32767 or from 61000 to 65535`}},
		{"a resolver on the unspecified address", replace(doc("Resolver", "resolver", `{"listen": ["::"], "allow": ["2001:db8::/32"]}`)),
			[]string{`Resolver/resolver: spec.listen: the address :: is no unicast address the resolver can listen on`}},
		{"a forwarder on loopback", replace(doc("Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32"], "forwarders": ["127.0.0.53"]}`)),
			[]string{`Resolver/resolver: spec.forwarders: the forwarder 127.0.0.53 is no unicast address`}},
		{"a resolver on an IPv4-mapped address", replace(doc("Resolver", "resolver", `{"listen": ["::ffff:192.0.2.1"], "allow": ["2001:db8::/32"]}`)),
			[]string{`Resolver/resolver: spec.listen: the address ::ffff:192.0.2.1 is no unicast address the resolver can listen on`}},
		{"a DNS64 prefix of 95 bits", replace(doc("Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32"], "dns64": {"prefix": "64:ff9b::/95"}}`)),
			[]string{`Resolver/resolver: spec.dns64: the prefix 64:ff9b::/95 is no NAT64 prefix; give an IPv6 range of 32, 40, 48, 56, 64 or 96 bits, such as 64:ff9b::/96`}},
		{"a record outside its zone", replace(doc("Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32"], "localZones": [{"name": "lan.example.org", "records": [{"name": "xlan.example.org", "type": "A", "value": "192.0.2.1"}]}]}`)),
			[]string{`Resolver/resolver: spec.localZones: the local zone lan.example.org holds a record of xlan.example.org, which is outside it`}},
		{"a prefix that sets bits 64 to 71", replace(doc("NAT64", "lab", `{"prefix": "2001:db8:64:0:100::/96"}`)),
			[]string{`NAT64/lab: spec.prefix: the prefix 2001:db8:64:0:100::/96 sets bits 64 to 71, which RFC 6052 keeps zero`}},
		{"an IPv4-mapped prefix", replace(doc("NAT64", "lab", `{"prefix": "::ffff:0:0/96"}`)),
			[]string{`NAT64/lab: spec.prefix: the prefix ::ffff:0:0/96 is no NAT64 prefix; give an IPv6 range of 32, 40, 48, 56, 64 or 96 bits, such as 64:ff9b::/96`}},
		{"a DNS64 prefix that sets bits 64 to 71", replace(doc("Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32"], "dns64": {"prefix": "64:ff9b::ff00:0:0:0/96"}}`)),
			[]string{`Resolver/resolver: spec.dns64: the prefix 64:ff9b::ff00:0:0:0/96 sets bits 64 to 71, which RFC 6052 keeps zero`}},
		{"a pool of loopback addresses", replace(doc("NAT64", "lab", `{"prefix": "2001:db8:64::/96", "pool4": [{"prefix": "127.0.0.1/32"}]}`)),
			[]string{`NAT64/lab: spec.pool4: the pool entry 127.0.0.1/32 holds addresses no connection leaves from: the unspecified address, loopback, multicast or broadcast`}},
		{"a pool of the broadcast address", replace(doc("NAT64", "lab", `{"prefix": "2001:db8:64::/96", "pool4": [{"prefix": "255.255.0.0/16"}]}`)),
			[]string{`NAT64/lab: spec.pool4: the pool entry 255.255.0.0/16 holds addresses no connection leaves from: the unspecified address, loopback, multicast or broadcast`}},
		{"two entries of one protocol's addresses and ports", replace(doc("NAT64", "lab", `{"prefix": "2001:db8:64::/96", "pool4": [{"prefix": "203.0.113.0/30", "ports": "61001-62000", "protocols": ["tcp"]}, {"prefix": "203.0.113.1/32", "ports": 61500, "protocols": ["udp", "tcp"]}]}`)),
			[]string{`NAT64/lab: spec.pool4: the pool entry 203.0.113.0/30 shares tcp addresses and ports with another; give each entry of a protocol addresses or ports of its own`}},
		{"an A record of IPv6", replace(resolver(record("A", "2001:db8::1"))), recordMessage("A", "2001:db8::1", "IPv4 address")},
		{"an AAAA record of IPv4", replace(resolver(record("AAAA", "192.0.2.1"))), recordMessage("AAAA", "192.0.2.1", "IPv6 address")},
		{"an IPv4-mapped AAAA record", replace(resolver(record("AAAA", "::ffff:192.0.2.1"))), recordMessage("AAAA", "::ffff:192.0.2.1", "IPv6 address")},
		{"an AAAA record of a zone", replace(resolver(record("AAAA", "fe80::1%eth0"))), recordMessage("AAAA", "fe80::1%eth0", "IPv6 address")},
		{"an A record of no address", replace(resolver(record("A", "foo"))), recordMessage("A", "foo", "IPv4 address")},
		{"a CNAME in capitals", replace(resolver(record("CNAME", "Mail.lan.example.org"))),
			recordMessage("CNAME", "Mail.lan.example.org", "DNS name in lower case without the root's dot")},
		{"a CNAME of the root's dot", replace(resolver(record("CNAME", "mail.lan.example.org."))),
			recordMessage("CNAME", "mail.lan.example.org.", "DNS name in lower case without the root's dot")},
		{"a PTR of no name", replace(resolver(record("PTR", "a b"))), recordMessage("PTR", "a b", "DNS name in lower case without the root's dot")},
		{"an MX record without a name", replace(resolver(record("MX", "10"))),
			recordMessage("MX", "10", "preference from 0 to 65535 and DNS name, a single space apart")},
		{"an MX record of a leading zero", replace(resolver(record("MX", "010 mail.lan.example.org"))),
			recordMessage("MX", "010 mail.lan.example.org", "preference from 0 to 65535 and DNS name, a single space apart")},
		{"an SRV record of a big port", replace(resolver(record("SRV", "0 1 65536 mail.lan.example.org"))),
			recordMessage("SRV", "0 1 65536 mail.lan.example.org", "priority, weight and port from 0 to 65535 and DNS name, a single space apart")},
		{"an SRV record of two spaces", replace(resolver(record("SRV", "0 1  993 mail.lan.example.org"))),
			recordMessage("SRV", "0 1  993 mail.lan.example.org", "priority, weight and port from 0 to 65535 and DNS name, a single space apart")},
		// The output unbound compares the records in pairs.
		{"a record twice", replace(resolver(record("AAAA", "2001:db8::1") + ", " + record("AAAA", "2001:DB8::1"))), nil},
		{"a CNAME beside another record", replace(resolver(record("CNAME", "r1.lan.example.org") + ", " + record("TXT", "x"))), nil},
		{"a name of 254 characters", replace(resolver(`{"name": "` + long + `", "type": "A", "value": "192.0.2.1"}`)),
			[]string{`Resolver/resolver: spec.localZones: the name ` + long + ` holds more than 253 characters`}},
		{"two NAT64s of empty pools", replace(doc("NAT64", "edge", `{}`), doc("NAT64", "lab", `{"prefix": "2001:db8:64::/96"}`)), nil},
		{"a link-local prefix", replace(doc("NAT64", "lab", `{"prefix": "fe80::/64"}`)),
			[]string{`NAT64/lab: spec.prefix: the prefix fe80::/64 holds link-local, multicast or unspecified addresses`}},
		{"a multicast prefix", replace(doc("NAT64", "lab", `{"prefix": "ff0e::/96"}`)),
			[]string{`NAT64/lab: spec.prefix: the prefix ff0e::/96 holds link-local, multicast or unspecified addresses`}},
		{"the unspecified prefix", replace(doc("NAT64", "lab", `{"prefix": "::/96"}`)),
			[]string{`NAT64/lab: spec.prefix: the prefix ::/96 holds link-local, multicast or unspecified addresses`}},
		{"a resolver on a link-local address", replace(doc("Resolver", "resolver", `{"listen": ["fe80::1"], "allow": ["2001:db8::/32"]}`)),
			[]string{`Resolver/resolver: spec.listen: the address fe80::1 is no unicast address the resolver can listen on`}},
		{"a link-local forwarder", replace(doc("Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32"], "forwarders": ["169.254.0.53"]}`)),
			[]string{`Resolver/resolver: spec.forwarders: the forwarder 169.254.0.53 is no unicast address`}},
		{"ipv4only.arpa as a local zone", replace(doc("Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32"], "dns64": {}, "localZones": [{"name": "arpa"}]}`)),
			[]string{"Resolver resolver has a local zone over ipv4only.arpa, which Unbound would answer before DNS64; drop the zone or DNS64"}},
		{"ipv4only.arpa without DNS64", replace(doc("Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32"], "localZones": [{"name": "ipv4only.arpa"}]}`)), nil},
		// The rules loop over the records once, so many of them stay within chalkd's runtime
		// cost limit.
		{"many AAAA records", replace(resolver(many(manyRecords, "AAAA"))), nil},
		{"many TXT records", replace(resolver(many(manyRecords, "TXT"))), nil},
		{"a redirect zone's record below it", replace(doc("Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32"], "localZones": [{"name": "lan.example.org", "type": "redirect", "records": [{"name": "a.lan.example.org", "type": "A", "value": "192.0.2.1"}]}]}`)),
			[]string{`Resolver/resolver: spec.localZones: the redirect zone lan.example.org holds a record of a.lan.example.org; a redirect zone's records are of its own name, which Unbound answers for every name below it`}},
		{"a redirect zone of its own name", replace(doc("Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["2001:db8::/32"], "localZones": [{"name": "lan.example.org", "type": "redirect", "records": [{"name": "lan.example.org", "type": "A", "value": "192.0.2.1"}]}]}`)), nil},
		{"listen and allow of other families", replace(doc("Resolver", "resolver", `{"listen": ["2001:db8:ffff::1"], "allow": ["192.0.2.0/24"]}`)),
			[]string{"Resolver resolver listens on no address of a family its clients' ranges are of, so it would answer no one; give listen and allow a family in common"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failures, err := set.Evaluate(Input{Documents: tc.docs, Node: "r1"})
			if err != nil {
				t.Fatal(err)
			}
			if got := messages(failures); !slices.Equal(got, tc.want) {
				t.Errorf("failures:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

// A pool entry's port written 61001.0 or 6.1001e4 is the int 61001 the rules take, as the
// documents hold an integer in plain digits however the config writes it.
func TestPoolPortOfAnIntegerSpelling(t *testing.T) {
	set, doc := routerRules(t)
	for _, port := range []string{"61001.0", "6.1001e4"} {
		n := doc("NAT64", "lab", `{"prefix": "2001:db8:64::/96", "pool4": [{"prefix": "203.0.113.1/32", "ports": 61001}]}`)
		n.Object["spec"].(map[string]any)["pool4"].([]any)[0].(map[string]any)["ports"] = json.Number(port)
		n = config.FromObject(n.Object)
		failures, err := set.Evaluate(Input{Documents: []config.Document{n}, Node: "r1"})
		if err != nil || len(failures) > 0 {
			t.Errorf("ports %s: failures %v, err %v", port, messages(failures), err)
		}
	}
}

// The schemas fill in a NAT64's well-known prefix and a pool entry's ports and protocols, and take
// local zones' names in lower case without the root's dot and record data without quotes,
// backslashes or line breaks.
func TestNAT64Schemas(t *testing.T) {
	s, err := schema.LoadSchemas("../../test/fixtures/kinds/router")
	if err != nil {
		t.Fatal(err)
	}
	filled, err := s.Default(builtinDoc(t, derive.API, "NAT64", "edge", `{"pool4": [{"prefix": "192.0.2.10/32"}]}`).Object)
	if err != nil {
		t.Fatal(err)
	}
	spec := filled["spec"].(map[string]any)
	entry := spec["pool4"].([]any)[0].(map[string]any)
	if spec["prefix"] != "64:ff9b::/96" || entry["ports"] != "61001-65535" || len(entry["protocols"].([]any)) != 3 {
		t.Errorf("defaults: %v", spec)
	}
	for _, tc := range []struct {
		spec string
		ok   bool
	}{
		{`{"listen": ["2001:db8::1"], "allow": ["2001:db8::/32"], "localZones": [{"name": "_tcp.lan.example.org"}]}`, true},
		{`{"listen": ["2001:db8::1"], "allow": ["2001:db8::/32"], "localZones": [{"name": "Lan.example.org"}]}`, false},
		{`{"listen": ["2001:db8::1"], "allow": ["2001:db8::/32"], "localZones": [{"name": "lan.example.org."}]}`, false},
		{`{"listen": ["2001:db8::1"], "allow": ["2001:db8::/32"], "localZones": [{"name": "lan.example.org", "records": [{"name": "a.lan.example.org", "type": "TXT", "value": "two\nlines"}]}]}`, false},
		{`{"listen": ["2001:db8::1"], "allow": ["2001:db8::/32"], "localZones": [{"name": "lan.example.org", "records": [{"name": "a.lan.example.org", "type": "TXT", "value": "[a]^b~ c@d"}]}]}`, true},
		{`{"listen": ["2001:db8::1"], "allow": ["2001:db8::/32"], "localZones": [{"name": "lan.example.org", "records": [{"name": "a.lan.example.org", "type": "TXT", "value": "say \"hi\""}]}]}`, false},
		{`{"listen": ["2001:db8::1"], "allow": ["2001:db8::/32"], "localZones": [{"name": "lan.example.org", "records": [{"name": "a.lan.example.org", "type": "TXT", "value": "it's"}]}]}`, false},
		{`{"listen": ["2001:db8::1"], "allow": ["2001:db8::/32"], "localZones": [{"name": "lan.example.org", "records": [{"name": "a.lan.example.org", "type": "TXT", "value": "a\\b"}]}]}`, false},
		{`{"listen": [], "allow": ["2001:db8::/32"]}`, false},
		{`{"listen": ["2001:db8::1"], "allow": []}`, false},
	} {
		d := builtinDoc(t, derive.API, "Resolver", "resolver", tc.spec)
		if err := s.Validate(d.Object); (err == nil) != tc.ok {
			t.Errorf("Resolver %s: err = %v, want accepted %v", tc.spec, err, tc.ok)
		}
	}
}

// The NAT64 rules refuse a pool entry that holds a static IPv4 address a Network document gives a
// link, in [Network] or [Address] Address= or [DHCPServer] ServerAddress=, with or without its
// length and spaces, which the router answers itself, and a static address in 169.254.64.0/18,
// the NAT64s' links' range, beside NAT64s. Values that are no address, IPv6 addresses and
// addresses outside the pools pass, and so does the range without NAT64s.
func TestNAT64StaticAddresses(t *testing.T) {
	set, doc := routerRules(t)
	nat64 := doc("NAT64", "edge", `{"pool4": [{"prefix": "192.0.2.8/30"}]}`)
	net := func(name, spec string) config.Document {
		return builtinDoc(t, config.NetworkdAPI, "Network", name, spec)
	}
	pool := func(a string) []string {
		return []string{"NAT64 edge: the pool entry 192.0.2.8/30 holds " + a + ", an address a Network document gives a link; the router answers it itself, so leave pool4 empty to take the address of the link the connections leave through"}
	}
	linkRange := []string{"a Network document gives a link the address 169.254.100.1, which lies in 169.254.64.0/18, the range of the NAT64s' links; give the link another address"}
	// many are n Network documents of four addresses each outside the pool, and one of an
	// address in it, which the rules compare at the highest cost.
	many := func(n int) []config.Document {
		var out []config.Document
		for i := range n {
			out = append(out, net(fmt.Sprintf("%03d-lan", i), fmt.Sprintf(`{"match": {"name": "eth%d"}, "network": {"address": ["10.%d.0.1/24", "10.%d.1.1/24", "2001:db8:%x::1/64"]}, "address": [{"address": "10.%d.2.1/24"}]}`, i, i, i, i, i)))
		}
		return append(out, net("999-wan", `{"match": {"name": "wan"}, "dhcpServer": {"serverAddress": "192.0.2.11"}}`))
	}
	for _, tc := range []struct {
		name string
		docs []config.Document
		want []string
	}{
		{"an address of [Network]", []config.Document{nat64, net("20-wan", `{"match": {"name": "eth0"}, "network": {"address": ["2001:db8::1/64", "192.0.2.9/24"]}}`)}, pool("192.0.2.9")},
		{"an address of [Address]", []config.Document{nat64, net("20-wan", `{"match": {"name": "eth0"}, "address": {"address": "192.0.2.10"}}`)}, pool("192.0.2.10")},
		{"[Address] sections", []config.Document{nat64, net("20-wan", `{"match": {"name": "eth0"}, "address": [{"address": "198.51.100.1/24"}, {"address": "192.0.2.10/24"}]}`)}, pool("192.0.2.10")},
		{"an address between spaces", []config.Document{nat64, net("20-wan", `{"match": {"name": "eth0"}, "network": {"address": " 192.0.2.9/24 "}}`)}, pool("192.0.2.9")},
		{"a DHCP server's address", []config.Document{nat64, net("20-wan", `{"match": {"name": "eth0"}, "dhcpServer": {"serverAddress": "192.0.2.11/24"}}`)}, pool("192.0.2.11")},
		{"addresses beside the pool", []config.Document{nat64, net("20-wan", `{"match": {"name": "eth0"}, "network": {"address": ["192.0.2.12/24", "", "dhcp", "999.0.2.9/24", "192.0.2.009", "2001:db8::/64", 7], "dhcp": "yes"}}`), net("30-lan", `{"match": {"name": "eth1"}}`)}, nil},
		{"an address in the links' range", []config.Document{nat64, net("30-lan", `{"match": {"name": "eth1"}, "network": {"address": "169.254.100.1/24"}}`)}, linkRange},
		{"the links' range without NAT64s", []config.Document{net("30-lan", `{"match": {"name": "eth1"}, "network": {"address": "169.254.100.1/24"}}`)}, nil},
		{"link-local beside the range", []config.Document{nat64, net("30-lan", `{"match": {"name": "eth1"}, "network": {"address": "169.254.0.1/16"}}`)}, nil},
		{"many Networks", append(many(60), nat64, doc("NAT64", "lab", `{"prefix": "2001:db8:64::/96", "pool4": [{"prefix": "203.0.113.0/28"}, {"prefix": "203.0.113.16/28"}, {"prefix": "203.0.113.32/28"}, {"prefix": "203.0.113.48/28"}]}`)), pool("192.0.2.11")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failures, err := set.Evaluate(Input{Documents: tc.docs, Node: "r1"})
			if err != nil {
				t.Fatal(err)
			}
			if got := messages(failures); !slices.Equal(got, tc.want) {
				t.Errorf("failures:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}
