// Package router reads a router's state: FRR's BGP sessions and paths of each VRF from vtysh's
// JSON, and the kernel's routes from netlink.
package router

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// The vtysh commands whose JSON the parsers read.
const (
	// RoutesCommand is the command of the default VRF's paths of a family, ipv4 or ipv6.
	RoutesCommand = "show bgp %s unicast detail json"
	// VRFRoutesCommand is the command of a VRF's paths of a family: the VRF's name, then ipv4 or
	// ipv6.
	VRFRoutesCommand = "show bgp vrf %s %s unicast detail json"
)

// path is a path as show bgp [vrf <name>] <family> unicast detail json prints it.
type path struct {
	// ImportedFrom is set for a path the instance imports from another VRF's, whose name
	// NHVRFName holds.
	ImportedFrom string `json:"importedFrom"`
	NHVRFName    string `json:"nhVrfName"`

	ASPath struct {
		String string `json:"string"`
	} `json:"aspath"`
	Metric    uint32  `json:"metric"`
	LocalPref *uint32 `json:"locPrf"`
	Valid     bool    `json:"valid"`
	Multipath bool    `json:"multipath"`
	// Local is set for a network or aggregate of the instance and for a path it imports from
	// another VRF, and Sourced for a network, a route it redistributes and an imported path; FRR
	// marks neither on a path a peer sent.
	Local   bool `json:"local"`
	Sourced bool `json:"sourced"`
	Best    *struct {
		Overall bool `json:"overall"`
	} `json:"bestpath"`
	NextHops []struct {
		IP string `json:"ip"`
	} `json:"nexthops"`
	Peer struct {
		PeerID    string `json:"peerId"`
		Interface string `json:"interface"`
	} `json:"peer"`
	Community *struct {
		List []string `json:"list"`
	} `json:"community"`
	LargeCommunity *struct {
		List []string `json:"list"`
	} `json:"largeCommunity"`
}

// ParseRoutes reads the paths of show bgp [vrf <name>] <family> unicast detail json into the
// routes BGP selected: the best path of each prefix and the equal paths installed beside it, by
// prefix. FRR prints a warning and no routes without a BGP instance.
func ParseRoutes(data []byte) ([]*BGPRoute, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("bgpd did not answer")
	}
	var table struct {
		Routes map[string][]path `json:"routes"`
	}
	if err := json.Unmarshal(data, &table); err != nil {
		return nil, fmt.Errorf("read FRR's BGP paths: %w", err)
	}
	var routes []*BGPRoute
	for prefix, paths := range table.Routes {
		for _, p := range paths {
			best := p.Best != nil && p.Best.Overall
			if !p.Valid || !best && !p.Multipath {
				continue
			}
			r := &BGPRoute{Prefix: prefix, MED: p.Metric, LocalPref: p.LocalPref, Best: best}
			switch {
			case p.ImportedFrom != "":
				// FRR marks an imported path local; it keeps the AS path it had in its VRF, where
				// the router's own networks have the path Local. A path that names no VRF of its
				// next hop names the route distinguisher it came from.
				r.ImportedFrom = cmp.Or(p.NHVRFName, p.ImportedFrom)
				if p.ASPath.String != "Local" {
					r.ASPath = p.ASPath.String
				}
			case !p.Local && !p.Sourced:
				r.ASPath = p.ASPath.String
				r.Peer = cmp.Or(p.Peer.Interface, p.Peer.PeerID)
			}
			for _, h := range p.NextHops {
				if h.IP != "" && !slices.Contains(r.NextHops, h.IP) {
					r.NextHops = append(r.NextHops, h.IP)
				}
			}
			if p.Community != nil {
				r.Communities = append(r.Communities, p.Community.List...)
			}
			if p.LargeCommunity != nil {
				r.Communities = append(r.Communities, p.LargeCommunity.List...)
			}
			routes = append(routes, r)
		}
	}
	slices.SortStableFunc(routes, func(a, b *BGPRoute) int {
		if c := comparePrefixes(a.Prefix, b.Prefix); c != 0 {
			return c
		}
		// The best path first.
		switch {
		case a.Best && !b.Best:
			return -1
		case b.Best && !a.Best:
			return 1
		}
		return strings.Compare(a.Peer, b.Peer)
	})
	return routes, nil
}

// comparePrefixes orders address ranges by family, address and length, and other strings after
// them.
func comparePrefixes(a, b string) int {
	pa, errA := netip.ParsePrefix(a)
	pb, errB := netip.ParsePrefix(b)
	switch {
	case errA != nil || errB != nil:
		return strings.Compare(a, b)
	case pa.Addr() != pb.Addr():
		return pa.Addr().Compare(pb.Addr())
	}
	return pa.Bits() - pb.Bits()
}
