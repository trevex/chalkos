package router

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/router/keaconf"
)

// The routes of the prefixes Kea's DHCPv6 servers delegate. Kea routes none of them: its
// run_script hook runs chalkos-dhcp6-pd as the user kea, which records each delegated prefix in
// PDRecordDir as a file <link>_<address>_<length> that holds the link-local address that asked
// for it and when its lease ends, in seconds since 1970, and removes the file when the lease is
// released or expires. chalkd router dhcp6-routes routes each recorded prefix the rendered configs
// delegate on its link to that address, and removes the other routes of protocol PDProtocol. It
// runs as root and trusts no record: it reads the directory without following links, reads
// regular files alone and a few bytes of each, and removes no file.

// PDRecordDir is the directory chalkos-dhcp6-pd records the delegated prefixes in. The user kea
// owns it, in a directory it cannot write, so that it cannot put another directory in its place.
const PDRecordDir = "/var/lib/chalkos-dhcp6-pd"

// MaxPDRecords is the number of records that have not ended that ReadPDRecords routes at most,
// the delegable prefixes of all pools, which the renderer of the output kea keeps within it, and
// MaxPDRecordSize the bytes of a record it reads at most; a longer record is none.
const (
	MaxPDRecords    = 65536
	MaxPDRecordSize = 128
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
	var c keaconf.Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("read Kea's config: %w", err)
	}
	if c.Dhcp6 == nil {
		return nil, nil
	}
	var out []PDPool
	for _, s := range c.Dhcp6.Subnet6 {
		for _, p := range s.PDPools {
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

// pdRoute is the route of the record of the link and the prefix that holds data, and whether it
// is one: a link-local address and an end after now, of a prefix a pool of pools delegates on
// the link. The route ends with the lease, and at the latest when a lease of the pool would.
func pdRoute(link string, prefix netip.Prefix, data []byte, pools []PDPool, now time.Time) (PDRoute, bool) {
	r := PDRoute{Link: link, Prefix: prefix}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return r, false
	}
	var err error
	if r.Via, err = netip.ParseAddr(fields[0]); err != nil || !r.Via.Is6() || !r.Via.IsLinkLocalUnicast() || r.Via.Zone() != "" {
		return r, false
	}
	ends, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return r, false
	}
	r.Ends = time.Unix(ends, 0)
	if !r.Ends.After(now) {
		return r, false
	}
	for _, p := range pools {
		if p.Link == link && p.Length == prefix.Bits() && p.Range.Contains(prefix.Addr()) {
			r.VRF = p.VRF
			if p.Valid > 0 && r.Ends.After(now.Add(p.Valid)) {
				r.Ends = now.Add(p.Valid)
			}
			return r, true
		}
	}
	return r, false
}

// pdRecordLive reports whether the record data holds an end after now.
func pdRecordLive(data []byte, now time.Time) bool {
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return false
	}
	ends, err := strconv.ParseInt(fields[1], 10, 64)
	return err == nil && time.Unix(ends, 0).After(now)
}
