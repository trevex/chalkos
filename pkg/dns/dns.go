// Package dns holds the syntax of the DNS names and record data that a router's Resolver
// documents give their local zones. The rules' isDNSName() and isRecordData() check a config with
// it, and chalkos-render's output unbound guards the names and data it writes into Unbound's
// config with it, so the two cannot drift apart. It takes the standard library alone, as
// chalkos-render does.
package dns

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// name is a DNS name in lower case without the root's dot, as the Resolver's local zones and
// records name them: labels of letters, digits, dashes and underscores, which SRV records' names
// take, of 63 characters at most, that start with a letter, a digit or an underscore and end with
// a letter or a digit, or are an underscore alone.
var name = regexp.MustCompile(`^([a-z0-9_]([-a-z0-9_]{0,61}[a-z0-9])?\.)*[a-z0-9_]([-a-z0-9_]{0,61}[a-z0-9])?$`)

// IsName tells whether s is a DNS name of 253 characters at most in lower case without the root's
// dot. Such a name holds no quote, space, hash or line break, which Unbound's config would read as
// syntax.
func IsName(s string) bool {
	return len(s) <= 253 && name.MatchString(s)
}

// IsRecordData tells whether v is data a record of the type t holds, as Unbound's local-data takes
// it: an IPv4 address for A and an IPv6 address, no IPv4-mapped one, for AAAA, both without a
// zone; a name IsName takes for CNAME and PTR; a preference and a name for MX, and a priority, a
// weight, a port and a name for SRV, a single space apart, each number from 0 to 65535 in decimal
// without a sign or leading zeros; and for TXT 1 to 255 printable ASCII characters without quotes
// or backslashes. A type of none of these holds no data.
func IsRecordData(t, v string) bool {
	switch t {
	case "A", "AAAA":
		a, err := netip.ParseAddr(v)
		return err == nil && a.Zone() == "" && a.Is4() == (t == "A") && !a.Is4In6()
	case "CNAME", "PTR":
		return IsName(v)
	case "MX", "SRV":
		n := map[string]int{"MX": 1, "SRV": 3}[t]
		f := strings.Split(v, " ")
		if len(f) != n+1 {
			return false
		}
		for _, x := range f[:n] {
			if u, err := strconv.ParseUint(x, 10, 16); err != nil || strconv.FormatUint(u, 10) != x {
				return false
			}
		}
		return IsName(f[n])
	case "TXT":
		return v != "" && len(v) <= 255 && !strings.ContainsAny(v, "\"'\\") &&
			strings.IndexFunc(v, func(r rune) bool { return r < ' ' || r > '~' }) < 0
	}
	return false
}
