// Package dns holds the syntax of the DNS names and record data of a Resolver's local zones. The
// rules and chalkos-render's Unbound output share it, so the two cannot drift apart; it takes the
// standard library alone, as chalkos-render does.
package dns

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// name is a lower-case DNS name without the root's dot. Its labels may hold underscores, which
// SRV records' names take.
var name = regexp.MustCompile(`^([a-z0-9_]([-a-z0-9_]{0,61}[a-z0-9])?\.)*[a-z0-9_]([-a-z0-9_]{0,61}[a-z0-9])?$`)

// IsName tells whether s is a DNS name of 253 characters at most in lower case without the root's
// dot. Such a name holds no quote, space, hash or line break, which Unbound's config would read as
// syntax.
func IsName(s string) bool {
	return len(s) <= 253 && name.MatchString(s)
}

// IsRecordData tells whether v is valid Unbound local-data for a record of type t.
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
