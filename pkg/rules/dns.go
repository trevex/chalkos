package rules

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// dnsName is a DNS name in lower case without the root's dot, as the Resolver's local zones and
// records name them: labels of letters, digits, dashes and underscores, which SRV records' names
// take, of 63 characters at most, that start with a letter, a digit or an underscore and end with
// a letter or a digit, or are an underscore alone.
var dnsName = regexp.MustCompile(`^([a-z0-9_]([-a-z0-9_]{0,61}[a-z0-9])?\.)*[a-z0-9_]([-a-z0-9_]{0,61}[a-z0-9])?$`)

// isDNSName tells whether s is a name dnsName matches of 253 characters at most, as isDNSName()
// reads it.
func isDNSName(s string) bool {
	return len(s) <= 253 && dnsName.MatchString(s)
}

// isRecordData tells whether v is data a record of the type t holds, as isRecordData() reads it
// and Unbound's local-data takes it: an IPv4 address for A and an IPv6 address, no IPv4-mapped
// one, for AAAA, both without a zone; a DNS name for CNAME and PTR; a preference and a name for
// MX, and a priority, a weight, a port and a name for SRV, a single space apart, each number from
// 0 to 65535 in decimal without a sign or leading zeros; and for TXT 1 to 255 printable ASCII
// characters without quotes or backslashes. A type of none of these holds no data.
func isRecordData(t, v string) bool {
	switch t {
	case "A", "AAAA":
		a, err := netip.ParseAddr(v)
		return err == nil && a.Zone() == "" && a.Is4() == (t == "A") && !a.Is4In6()
	case "CNAME", "PTR":
		return isDNSName(v)
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
		return isDNSName(f[n])
	case "TXT":
		return v != "" && len(v) <= 255 && !strings.ContainsAny(v, "\"'\\") &&
			strings.IndexFunc(v, func(r rune) bool { return r < ' ' || r > '~' }) < 0
	}
	return false
}
