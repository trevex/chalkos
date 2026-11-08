package main

import (
	"fmt"
	"net/netip"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/trevex/chalkos/pkg/dns"
	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/router/derive"
	"github.com/trevex/chalkos/pkg/router/services"
)

// unboundControlOf is the socket on which unbound-control, and chalkd through it, reach the
// resolver of vrf: unbound.ctl in its runtime directory, /run/unbound for the default VRF's and
// /run/unbound-<vrf> for a VRF's.
func unboundControlOf(vrf string) string {
	return "/run/" + services.Unbound.RuntimeDir(vrf) + "/unbound.ctl"
}

// ipv4OnlyZone is the zone RFC 8880 has a DNS64 resolver answer itself.
const ipv4OnlyZone = "ipv4only.arpa."

// storeFile is a path the renderer writes into Unbound's config between quotes: absolute, clean,
// and of characters that need no quoting.
var storeFile = regexp.MustCompile(`^(/[A-Za-z0-9+._-]+)+$`)

// renderUnbound writes Unbound's config of each Resolver, out/unbound.conf for the default VRF's
// and out/unbound-<vrf>.conf for a VRF's: the addresses it answers on and the clients it answers,
// DNSSEC validation with the root's trust anchor in the file trustAnchor, DNS64 with the zone
// ipv4only.arpa of the file zoneFile, the local zones, the forwarders, and the control socket. A
// node without a Resolver gets no file and runs no Unbound. The validator router refuses a listen
// address that Network documents give only to links of other VRFs; the set check has refused two
// resolvers of one VRF.
func renderUnbound(trustAnchor, zoneFile string, in map[string]any, out string) error {
	for _, f := range []string{trustAnchor, zoneFile} {
		if !storeFile.MatchString(f) || filepath.Clean(f) != f {
			return fmt.Errorf("%q is no absolute path of a file Unbound reads", f)
		}
	}
	d, err := derive.Build(documents(in))
	if err != nil {
		return err
	}
	declared := map[string]bool{derive.DefaultVRF: true}
	for _, v := range d.VRFs {
		declared[v.Name] = true
	}
	for _, r := range d.Resolvers {
		// The VRF names a file and a control socket. A VRF document's name holds the characters
		// Build takes alone.
		vrf, err := derive.VRFName(r.Spec.VRF)
		if err != nil {
			return derive.Within("Resolver "+r.Name, err)
		}
		if _, err := derive.MustGet(declared, "VRF", vrf); err != nil {
			return derive.Within("Resolver "+r.Name, err)
		}
		text, err := unboundConfig(r.Name, r.Spec, vrf, trustAnchor, zoneFile)
		if err != nil {
			return derive.Within("Resolver "+r.Name, err)
		}
		if err := writeFile(out, services.Unbound.File(vrf), []byte(text)); err != nil {
			return err
		}
	}
	return nil
}

// unboundConfig is Unbound's config of r, the spec of the Resolver called name, which serves vrf.
func unboundConfig(name string, r derive.ResolverSpec, vrf, trustAnchor, zoneFile string) (string, error) {
	var b strings.Builder
	from := "the node's Resolver"
	if vrf != derive.DefaultVRF {
		from = "the Resolver of VRF " + vrf
	}
	b.WriteString("# Unbound's config, rendered by chalkos-render unbound from " + from + ".\n" +
		"# unbound-control reload_keep_cache applies it.\n" +
		"server:\n" +
		"  username: \"\"\n" +
		"  chroot: \"\"\n" +
		"  pidfile: \"\"\n" +
		"  directory: \"/\"\n" +
		"  do-daemonize: no\n" +
		"  use-syslog: no\n" +
		"  ip-freebind: yes\n" +
		"  port: 53\n")
	listen, allow, err := resolverAddresses(r)
	if err != nil {
		return "", err
	}
	for _, a := range listen {
		fmt.Fprintf(&b, "  interface: %s\n", a)
	}
	// Unbound refuses clients outside allow by default; the config says so itself. Of two lines of
	// one range the later decides, so an allow of a whole family opens it.
	b.WriteString("  access-control: 0.0.0.0/0 refuse\n  access-control: ::/0 refuse\n")
	for _, p := range allow {
		fmt.Fprintf(&b, "  access-control: %s allow\n", p)
	}
	modules := []string{"iterator"}
	if r.DNSSEC {
		modules = append([]string{"validator"}, modules...)
	}
	if r.DNS64 != nil {
		modules = append([]string{"dns64"}, modules...)
	}
	fmt.Fprintf(&b, "  module-config: %q\n", strings.Join(modules, " "))
	if r.DNSSEC {
		fmt.Fprintf(&b, "  trust-anchor-file: %q\n", trustAnchor)
	}
	if r.DNS64 != nil {
		p, err := derive.NAT64Prefix(r.DNS64.Prefix)
		if err != nil {
			return "", derive.Within("dns64", err)
		}
		fmt.Fprintf(&b, "  dns64-prefix: %s\n", p)
		// The zone of ipv4only.arpa below holds no signatures.
		if r.DNSSEC {
			fmt.Fprintf(&b, "  domain-insecure: %q\n", ipv4OnlyZone)
		}
	}
	records := renderedRecords{keys: map[string]bool{}, types: map[string]string{}}
	for i, z := range r.LocalZones {
		lines, err := localZoneLines(name, i, z, records)
		if err != nil {
			return "", derive.Within(fmt.Sprintf("local zone %d", i+1), err)
		}
		for _, l := range lines {
			b.WriteString("  " + l + "\n")
		}
	}
	// The iterator answers ipv4only.arpa from the zone, which the dns64 module then synthesizes
	// AAAA records from, without asking another server.
	if r.DNS64 != nil {
		fmt.Fprintf(&b, "auth-zone:\n  name: %q\n  zonefile: %q\n  for-downstream: no\n  for-upstream: yes\n  fallback-enabled: no\n", ipv4OnlyZone, zoneFile)
	}
	if len(r.Forwarders) > 0 {
		b.WriteString("forward-zone:\n  name: \".\"\n")
		for _, a := range r.Forwarders {
			fmt.Fprintf(&b, "  forward-addr: %s\n", a)
		}
	}
	fmt.Fprintf(&b, "remote-control:\n  control-enable: yes\n  control-interface: %s\n", unboundControlOf(vrf))
	return b.String(), nil
}

// resolverAddresses are the addresses the resolver r listens on and the ranges of the clients it
// answers, masked, each once, as the set check has refused a repeat. Unbound's config and the
// firewall's ruleset write them as netip prints them. It guards, with an internal error, an empty
// listen or allow, for which the firewall would open port 53 on every address of the router or to
// every client.
func resolverAddresses(r derive.ResolverSpec) ([]netip.Addr, []netip.Prefix, error) {
	if len(r.Listen) == 0 {
		return nil, nil, derive.InternalErrorf("listen names no address, so the firewall would accept DNS on every address of the router")
	}
	if len(r.Allow) == 0 {
		return nil, nil, derive.InternalErrorf("allow names no range, so the firewall would accept DNS from every client")
	}
	allow := make([]netip.Prefix, len(r.Allow))
	for i, p := range r.Allow {
		allow[i] = p.Masked()
	}
	return r.Listen, allow, nil
}

// renderedRecords are the records of the local zones rendered so far: each as "<name>. <type>
// <data>", of which Unbound keeps one of two alike, and the type of each name's first record.
type renderedRecords struct {
	keys  map[string]bool
	types map[string]string
}

// localZoneLines are Unbound's local-zone line of z, the local zone at index zone of the Resolver
// called resolver, and a local-data line per record, and adds z's records to seen. It refuses a
// record that seen holds and a CNAME record of a name that holds another record, in any zone,
// which the rules leave to the renderer, as they would compare the records in pairs. A zone's name
// stands between double quotes, and a record between single quotes with a TXT record's text
// between double quotes; it guards, with an internal error, names and data that would end those
// quotes, start a comment or add a line.
func localZoneLines(resolver string, zone int, z derive.LocalZone, seen renderedRecords) ([]string, error) {
	if !dns.IsName(z.Name) {
		return nil, derive.InternalErrorf("%q is no DNS name in lower case without the root's dot", z.Name)
	}
	lines := []string{fmt.Sprintf("local-zone: \"%s.\" %s", z.Name, z.Type)}
	for i, r := range z.Records {
		at := fmt.Sprintf("%s: record %d", z.Name, i+1)
		if !dns.IsName(r.Name) {
			return nil, derive.InternalErrorf("%s: %q is no DNS name in lower case without the root's dot", at, r.Name)
		}
		value, err := recordValue(r.Type, r.Value)
		if err != nil {
			return nil, derive.Within(at, err)
		}
		key := fmt.Sprintf("%s. %s %s", r.Name, r.Type, value)
		if seen.keys[key] {
			return nil, refusals.Errorf("Resolver", resolver, fmt.Sprintf("spec.localZones[%d].records[%d]", zone, i), "%s appears twice; Unbound keeps one, so drop the other", key)
		}
		seen.keys[key] = true
		if t, ok := seen.types[r.Name]; ok && (t == "CNAME" || r.Type == "CNAME") {
			return nil, refusals.Errorf("Resolver", resolver, fmt.Sprintf("spec.localZones[%d].records[%d]", zone, i), "%s holds a CNAME record and another record; a CNAME's name holds it alone", r.Name)
		}
		seen.types[r.Name] = r.Type
		lines = append(lines, fmt.Sprintf("local-data: '%s. %d IN %s %s'", r.Name, r.TTL, r.Type, value))
	}
	return lines, nil
}

// recordValue is the data v of a record of type t as Unbound's local-data takes it: an address as
// netip prints it, a name with the root's dot, and a TXT record's text between double quotes. It
// guards, with an internal error, data that dns.IsRecordData refuses, as the rules do.
func recordValue(t, v string) (string, error) {
	if !dns.IsRecordData(t, v) {
		return "", derive.InternalErrorf("%q is no %s record's data", v, t)
	}
	switch t {
	case "A", "AAAA":
		return netip.MustParseAddr(v).String(), nil
	case "TXT":
		return `"` + v + `"`, nil
	}
	// CNAME, PTR, MX and SRV end in a name, after the numbers of MX and SRV.
	return v + ".", nil
}
