package router

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/router/derive"
)

// Kea routes none of the prefixes its DHCPv6 servers delegate. Its run_script hook runs
// chalkos-dhcp6-pd as the user kea, which records each delegated prefix in PDRecordDir as a file
// <link>_<address>_<length> holding the client's link-local address and the lease's end in Unix
// seconds, and removes the file when the lease ends. chalkd router dhcp6-routes runs as root,
// routes each recorded prefix the rendered configs delegate on its link, removes the other routes
// of protocol PDProtocol, and trusts no record: it follows no links, reads a few bytes of regular
// files alone, and removes no file.

// PDRecordDir is the directory chalkos-dhcp6-pd records the delegated prefixes in. The user kea
// owns it, in a directory it cannot write, so that it cannot put another directory in its place.
// That directory also holds PDRecordDir.new, where the hook writes a record before it renames it
// in, and Kea's sandbox binds it whole, so that the rename stays on one mount.
const PDRecordDir = "/var/lib/chalkos-dhcp6-pd/records"

// MaxPDRecords caps the live records ReadPDRecords routes; the renderer keeps all pools within
// it. maxPDRecordSize caps the bytes read of a record; a longer one is ignored.
const (
	MaxPDRecords    = derive.MaxDelegated
	maxPDRecordSize = 128
)

// PDProtocol is the protocol number of the routes of delegated prefixes, which routes lists as
// dhcp-pd.
const PDProtocol = 65

// PDPool is a range that an IPv6 subnet of a config of the output kea delegates prefixes of
// Length from to the clients on its link, in the VRF VRF, for leases of Valid at most.
type PDPool struct {
	VRF, Link string
	Range     netip.Prefix
	Length    int
	Valid     time.Duration
}

// ParseKea6Pools reads the ranges of delegated prefixes of a DHCPv6 config of the output kea,
// that of the server of vrf.
func ParseKea6Pools(vrf string, data []byte) ([]PDPool, error) {
	c, err := readKeaConfig(data)
	if err != nil {
		return nil, err
	}
	if c.Dhcp6 == nil {
		return nil, nil
	}
	var out []PDPool
	for _, s := range c.Dhcp6.Subnet6 {
		for _, p := range s.PDPools {
			if !p.Prefix.IsValid() {
				return nil, errors.New("read Kea's config: a pd-pool names no prefix")
			}
			r, err := p.Prefix.Prefix(p.PrefixLen)
			if err != nil {
				return nil, fmt.Errorf("read Kea's config: %w", err)
			}
			out = append(out, PDPool{VRF: vrf, Link: s.Interface, Range: r, Length: p.DelegatedLen, Valid: time.Duration(s.ValidLifetime) * time.Second})
		}
	}
	return out, nil
}

// PDRoute is the route of a delegated prefix: to the link-local address Via on the link Link,
// in the VRF VRF, until Ends.
type PDRoute struct {
	VRF, Link string
	Prefix    netip.Prefix
	Via       netip.Addr
	Ends      time.Time
}

// pdRecordName reads the name of a record: the link, whose name may hold underscores, and the
// delegated prefix.
func pdRecordName(name string) (string, netip.Prefix, bool) {
	i := strings.LastIndexByte(name, '_')
	if i < 0 {
		return "", netip.Prefix{}, false
	}
	j := strings.LastIndexByte(name[:i], '_')
	if j <= 0 {
		return "", netip.Prefix{}, false
	}
	link, address, length := name[:j], name[j+1:i], name[i+1:]
	a, err := netip.ParseAddr(address)
	if err != nil || !a.Is6() || a.Zone() != "" {
		return "", netip.Prefix{}, false
	}
	bits, err := strconv.Atoi(length)
	if err != nil {
		return "", netip.Prefix{}, false
	}
	p, err := a.Prefix(bits)
	if err != nil || p.Addr() != a {
		return "", netip.Prefix{}, false
	}
	return link, p, true
}

// pdRecord reads a record's end and, if it is one, its link-local address; via is invalid when
// the record names none.
func pdRecord(data []byte) (via netip.Addr, ends time.Time, ok bool) {
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return via, ends, false
	}
	end, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return via, ends, false
	}
	if a, err := netip.ParseAddr(fields[0]); err == nil && a.Is6() && a.IsLinkLocalUnicast() && a.Zone() == "" {
		via = a
	}
	return via, time.Unix(end, 0), true
}

// pdRoute is the route of a record to via until ends, if via is valid and a pool delegates the
// prefix on the link. The route ends no later than a lease of the pool would.
func pdRoute(link string, prefix netip.Prefix, via netip.Addr, ends time.Time, pools []PDPool, now time.Time) (PDRoute, bool) {
	if !via.IsValid() {
		return PDRoute{}, false
	}
	for _, p := range pools {
		if p.Link == link && p.Length == prefix.Bits() && p.Range.Contains(prefix.Addr()) {
			if p.Valid > 0 && ends.After(now.Add(p.Valid)) {
				ends = now.Add(p.Valid)
			}
			return PDRoute{VRF: p.VRF, Link: link, Prefix: prefix, Via: via, Ends: ends}, true
		}
	}
	return PDRoute{}, false
}
