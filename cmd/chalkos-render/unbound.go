package main

import (
	"fmt"
	"net/netip"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// unboundFile is the file renderUnbound writes.
const unboundFile = "unbound.conf"

// unboundControl is the socket on which unbound-control, and chalkd through it, reach Unbound.
const unboundControl = "/run/unbound/unbound.ctl"

// ipv4OnlyZone is the zone RFC 8880 has a DNS64 resolver answer itself.
const ipv4OnlyZone = "ipv4only.arpa."

// storeFile is a path the renderer writes into Unbound's config between quotes: absolute, clean,
// and of characters that need no quoting.
var storeFile = regexp.MustCompile(`^(/[A-Za-z0-9+._-]+)+$`)

// dnsName is a name in lower case without the root's dot, as the schema takes local zones' and
// records' names.
var dnsName = regexp.MustCompile(`^([a-z0-9_]([-a-z0-9_]{0,61}[a-z0-9])?\.)*[a-z0-9_]([-a-z0-9_]{0,61}[a-z0-9])?$`)

// localZoneTypes are the local zone types the schema takes, which Unbound names alike.
var localZoneTypes = []string{"static", "transparent", "typetransparent", "redirect", "refuse", "deny", "always_nxdomain"}

// renderUnbound writes Unbound's config of the node's Resolver to out/unbound.conf: the addresses
// it answers on and the clients it answers, DNSSEC validation with the root's trust anchor in the
// file trustAnchor, DNS64 with the zone ipv4only.arpa of the file zoneFile, the local zones, the
// forwarders, and the control socket. A node without a Resolver gets no file and runs no Unbound.
func renderUnbound(trustAnchor, zoneFile string, in map[string]any, out string) error {
	for _, f := range []string{trustAnchor, zoneFile} {
		if !storeFile.MatchString(f) || filepath.Clean(f) != f {
			return fmt.Errorf("%q is no absolute path of a file Unbound reads", f)
		}
	}
	d, err := readRouterDocuments(in)
	if err != nil {
		return err
	}
	switch len(d.Resolvers) {
	case 0:
		return nil
	case 1:
	default:
		return fmt.Errorf("the config holds %d Resolver documents; a node has one", len(d.Resolvers))
	}
	r := d.Resolvers[0]
	text, err := unboundConfig(r.Spec, trustAnchor, zoneFile)
	if err != nil {
		return fmt.Errorf("Resolver %s: %w", r.Name, err)
	}
	return writeFile(out, unboundFile, []byte(text))
}

func unboundConfig(r resolverSpec, trustAnchor, zoneFile string) (string, error) {
	var b strings.Builder
	b.WriteString("# Unbound's config, rendered by chalkos-render unbound from the node's Resolver.\n" +
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
		p, err := nat64Prefix(r.DNS64.Prefix)
		if err != nil {
			return "", fmt.Errorf("dns64: %w", err)
		}
		fmt.Fprintf(&b, "  dns64-prefix: %s\n", p)
		// The zone of ipv4only.arpa below holds no signatures.
		if r.DNSSEC {
			fmt.Fprintf(&b, "  domain-insecure: %q\n", ipv4OnlyZone)
		}
	}
	records := renderedRecords{keys: map[string]bool{}, types: map[string]string{}}
	for i, z := range r.LocalZones {
		lines, err := localZoneLines(z, records)
		if err != nil {
			return "", fmt.Errorf("local zone %d: %w", i+1, err)
		}
		if slices.ContainsFunc(r.LocalZones[:i], func(o localZone) bool { return o.Name == z.Name }) {
			return "", fmt.Errorf("local zone %d: %s appears twice", i+1, z.Name)
		}
		// Unbound answers a local zone before DNS64 sees the query.
		if v4 := strings.TrimSuffix(ipv4OnlyZone, "."); r.DNS64 != nil && (z.Name == v4 || strings.HasSuffix(z.Name, "."+v4) || strings.HasSuffix(v4, "."+z.Name)) {
			return "", fmt.Errorf("local zone %d: %s overlaps %s, which DNS64 answers itself", i+1, z.Name, v4)
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
		var forwarders []netip.Addr
		for _, a := range r.Forwarders {
			addr, err := unicastAddress(a)
			if err != nil {
				return "", fmt.Errorf("forwarders: %w", err)
			}
			if slices.Contains(forwarders, addr) {
				return "", fmt.Errorf("forwarders: %s appears twice", addr)
			}
			forwarders = append(forwarders, addr)
			fmt.Fprintf(&b, "  forward-addr: %s\n", addr)
		}
	}
	fmt.Fprintf(&b, "remote-control:\n  control-enable: yes\n  control-interface: %s\n", unboundControl)
	return b.String(), nil
}

// resolverAddresses are the addresses the resolver r listens on and the ranges of the clients it
// answers, masked, each once.
func resolverAddresses(r resolverSpec) ([]netip.Addr, []netip.Prefix, error) {
	if len(r.Listen) == 0 {
		return nil, nil, fmt.Errorf("it listens on no address")
	}
	var listen []netip.Addr
	for _, a := range r.Listen {
		addr, err := unicastAddress(a)
		if err != nil {
			return nil, nil, fmt.Errorf("listen: %w", err)
		}
		if slices.Contains(listen, addr) {
			return nil, nil, fmt.Errorf("listen: %s appears twice", addr)
		}
		listen = append(listen, addr)
	}
	if len(r.Allow) == 0 {
		return nil, nil, fmt.Errorf("it answers no client")
	}
	var allow []netip.Prefix
	for _, a := range r.Allow {
		p, err := netip.ParsePrefix(a)
		if err != nil {
			return nil, nil, fmt.Errorf("allow: %q is no address range", a)
		}
		p = p.Masked()
		if slices.Contains(allow, p) {
			return nil, nil, fmt.Errorf("allow: %s appears twice", p)
		}
		allow = append(allow, p)
	}
	if !slices.ContainsFunc(listen, func(a netip.Addr) bool {
		return slices.ContainsFunc(allow, func(p netip.Prefix) bool { return p.Addr().Is4() == a.Is4() })
	}) {
		return nil, nil, fmt.Errorf("it listens on no address of a family its clients' ranges are of, so it would answer no one")
	}
	return listen, allow, nil
}

// unicastAddress is a, if it is an address a resolver listens on or sends to: no unspecified,
// link-local, loopback, multicast or IPv4-mapped address, and no zone.
func unicastAddress(a string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(a)
	if err != nil || addr.Zone() != "" || addr.IsUnspecified() || addr.IsLinkLocalUnicast() || addr.IsLoopback() || addr.IsMulticast() || addr.Is4In6() {
		return netip.Addr{}, fmt.Errorf("%q is no unicast address", a)
	}
	return addr, nil
}

// renderedRecords are the records of the local zones rendered so far: each as "<name>. <type>
// <data>", of which Unbound keeps one of two alike, and the type of each name's first record.
type renderedRecords struct {
	keys  map[string]bool
	types map[string]string
}

// localZoneLines are Unbound's local-zone line of z and a local-data line per record. A record
// renders between single quotes, so a TXT record's text stands between double quotes. It refuses
// a record that seen holds, a CNAME record of a name that holds another record, and a record below
// a redirect zone's own name, and adds its records to seen.
func localZoneLines(z localZone, seen renderedRecords) ([]string, error) {
	if !dnsName.MatchString(z.Name) || len(z.Name) > 253 {
		return nil, fmt.Errorf("%q is no DNS name in lower case without the root's dot", z.Name)
	}
	if !slices.Contains(localZoneTypes, z.Type) {
		return nil, fmt.Errorf("%s: type %q is none of %s", z.Name, z.Type, strings.Join(localZoneTypes, ", "))
	}
	lines := []string{fmt.Sprintf("local-zone: \"%s.\" %s", z.Name, z.Type)}
	for i, r := range z.Records {
		at := fmt.Sprintf("%s: record %d", z.Name, i+1)
		if !dnsName.MatchString(r.Name) || len(r.Name) > 253 || (r.Name != z.Name && !strings.HasSuffix(r.Name, "."+z.Name)) {
			return nil, fmt.Errorf("%s: %q is no name of the zone", at, r.Name)
		}
		if z.Type == "redirect" && r.Name != z.Name {
			return nil, fmt.Errorf("%s: %s is no name of the redirect zone's own, which Unbound answers for every name below it", at, r.Name)
		}
		if r.TTL > 2147483647 {
			return nil, fmt.Errorf("%s: ttl %d is past 2147483647", at, r.TTL)
		}
		value, err := recordValue(r.Type, r.Value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", at, err)
		}
		key := fmt.Sprintf("%s. %s %s", r.Name, r.Type, value)
		if seen.keys[key] {
			return nil, fmt.Errorf("%s: %s appears twice", at, key)
		}
		seen.keys[key] = true
		if t, ok := seen.types[r.Name]; ok && (t == "CNAME" || r.Type == "CNAME") {
			return nil, fmt.Errorf("%s: %s holds a CNAME record and another record; a CNAME's name holds it alone", at, r.Name)
		}
		seen.types[r.Name] = r.Type
		lines = append(lines, fmt.Sprintf("local-data: '%s. %d IN %s %s'", r.Name, r.TTL, r.Type, value))
	}
	return lines, nil
}

// recordValue is the data of a record of type t as Unbound's local-data takes it, refusing what
// no record of the type holds.
func recordValue(t, v string) (string, error) {
	name := func(s string) (string, error) {
		if !dnsName.MatchString(s) || len(s) > 253 {
			return "", fmt.Errorf("%q is no DNS name in lower case without the root's dot", s)
		}
		return s + ".", nil
	}
	// numbers are the numbers of s, n of them, which are what, and the name that follows them.
	numbers := func(s string, n int, what string) ([]string, string, error) {
		f := strings.Split(s, " ")
		if len(f) != n+1 {
			return nil, "", fmt.Errorf("%q is not %s and a name, a single space apart", s, what)
		}
		for _, x := range f[:n] {
			if u, err := strconv.ParseUint(x, 10, 16); err != nil || strconv.FormatUint(u, 10) != x {
				return nil, "", fmt.Errorf("%q is no number from 0 to 65535", x)
			}
		}
		target, err := name(f[n])
		return f[:n], target, err
	}
	switch t {
	case "A", "AAAA":
		a, err := netip.ParseAddr(v)
		if err != nil || a.Zone() != "" || a.Is4() != (t == "A") || a.Is4In6() {
			return "", fmt.Errorf("%q is no %s address", v, map[string]string{"A": "IPv4", "AAAA": "IPv6"}[t])
		}
		return a.String(), nil
	case "CNAME", "PTR":
		return name(v)
	case "MX", "SRV":
		n := map[string]int{"MX": 1, "SRV": 3}[t]
		what := map[string]string{"MX": "a preference", "SRV": "a priority, a weight, a port"}[t]
		nums, target, err := numbers(v, n, what)
		if err != nil {
			return "", err
		}
		return strings.Join(append(nums, target), " "), nil
	case "TXT":
		if v == "" || len(v) > 255 || strings.ContainsAny(v, "\"'\\") || strings.IndexFunc(v, func(r rune) bool { return r < ' ' || r > '~' }) >= 0 {
			return "", fmt.Errorf("%q is no text of 1 to 255 printable characters without quotes or backslashes", v)
		}
		return `"` + v + `"`, nil
	}
	return "", fmt.Errorf("type %q is none of A, AAAA, CNAME, PTR, MX, SRV and TXT", t)
}
