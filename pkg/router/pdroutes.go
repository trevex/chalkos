package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The routes of the prefixes Kea's DHCPv6 servers delegate. Kea routes none of them: its
// run_script hook runs chalkos-dhcp6-pd, which records each delegated prefix in PDRecordDir as a
// file <link>_<address>_<length> that holds the link-local address that asked for it and when its
// lease ends, in seconds since 1970, and removes the file when the lease is released.
// chalkd router dhcp6-routes routes each recorded prefix the rendered configs delegate on its link
// to that address, and removes the other routes of protocol PDProtocol.

// PDRecordDir is the directory chalkos-dhcp6-pd records the delegated prefixes in.
const PDRecordDir = "/var/lib/kea/pd-routes"

// PDProtocol is the protocol number of the routes of delegated prefixes, which routes lists as
// dhcp-pd.
const PDProtocol = 65

// PDPool is a range that an IPv6 subnet of a config of the output kea delegates prefixes of
// Length from to the clients on its link, in the VRF VRF.
type PDPool struct {
	VRF, Link string
	Range     netip.Prefix
	Length    int
}

// ParseKea6Pools reads the ranges of delegated prefixes of a DHCPv6 config of the output kea,
// that of the server of vrf.
func ParseKea6Pools(vrf string, data []byte) ([]PDPool, error) {
	var c struct {
		Dhcp6 struct {
			Subnet6 []struct {
				Interface string `json:"interface"`
				PDPools   []struct {
					Prefix    string `json:"prefix"`
					Length    int    `json:"prefix-len"`
					Delegated int    `json:"delegated-len"`
				} `json:"pd-pools"`
			} `json:"subnet6"`
		} `json:"Dhcp6"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("read Kea's config: %w", err)
	}
	var out []PDPool
	for _, s := range c.Dhcp6.Subnet6 {
		for _, p := range s.PDPools {
			a, err := netip.ParseAddr(p.Prefix)
			if err != nil {
				return nil, fmt.Errorf("read Kea's config: %w", err)
			}
			r, err := a.Prefix(p.Length)
			if err != nil {
				return nil, fmt.Errorf("read Kea's config: %w", err)
			}
			out = append(out, PDPool{VRF: vrf, Link: s.Interface, Range: r, Length: p.Delegated})
		}
	}
	return out, nil
}

// PDRoute is the route of a delegated prefix: to the link-local address Via on the link Link,
// in the VRF VRF, until the prefix's lease ends.
type PDRoute struct {
	VRF, Link string
	Prefix    netip.Prefix
	Via       netip.Addr
	Ends      time.Time
}

// ReadPDRecords reads the records of delegated prefixes in dir: the routes of those whose leases
// have not ended at now and that a pool of pools delegates on the record's link, by its range and
// length, to a link-local address, and stale, the files of the other records, which no route
// keeps. A missing dir holds no records; files whose names start with a dot are the hook's own,
// half written.
func ReadPDRecords(dir string, pools []PDPool, now time.Time) (routes []PDRoute, stale []string, err error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		r, ok := readPDRecord(path, e.Name(), pools)
		if !ok || !r.Ends.After(now) {
			stale = append(stale, path)
			continue
		}
		routes = append(routes, r)
	}
	return routes, stale, nil
}

// readPDRecord reads the record of the file path, named name, and reports whether it is one of a
// pool of pools.
func readPDRecord(path, name string, pools []PDPool) (PDRoute, bool) {
	var r PDRoute
	// The link's name may hold underscores; the address and the length hold none.
	i := strings.LastIndexByte(name, '_')
	if i < 0 {
		return r, false
	}
	j := strings.LastIndexByte(name[:i], '_')
	if j <= 0 {
		return r, false
	}
	link, address, length := name[:j], name[j+1:i], name[i+1:]
	a, err := netip.ParseAddr(address)
	if err != nil || !a.Is6() || a.Zone() != "" {
		return r, false
	}
	bits, err := strconv.Atoi(length)
	if err != nil {
		return r, false
	}
	if r.Prefix, err = a.Prefix(bits); err != nil || r.Prefix.Addr() != a {
		return r, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return r, false
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return r, false
	}
	if r.Via, err = netip.ParseAddr(fields[0]); err != nil || !r.Via.Is6() || !r.Via.IsLinkLocalUnicast() || r.Via.Zone() != "" {
		return r, false
	}
	ends, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return r, false
	}
	r.Ends, r.Link = time.Unix(ends, 0), link
	for _, p := range pools {
		if p.Link == link && p.Length == bits && p.Range.Contains(a) {
			r.VRF = p.VRF
			return r, true
		}
	}
	return r, false
}
