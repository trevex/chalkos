// Package router reads a router's state: FRR's BGP sessions and paths from vtysh's JSON, and the
// kernel's routes from netlink.
package router

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
)

// PeerGroupPrefix starts the name of the peer group frr.conf gives each BGPPeer document.
const PeerGroupPrefix = "peer-"

// The vtysh commands whose JSON the parsers read.
const (
	NeighborsCommand = "show bgp neighbors json"
	// RoutesCommand is the command of a family's paths, ipv4 or ipv6.
	RoutesCommand = "show bgp %s unicast detail json"
)

// neighbor is a session as show bgp neighbors json prints it.
type neighbor struct {
	PeerGroup         string `json:"peerGroup"`
	State             string `json:"bgpState"`
	UpMsec            uint64 `json:"bgpTimerUpMsec"`
	AddressFamilyInfo map[string]struct {
		Accepted uint32 `json:"acceptedPrefixCounter"`
		Sent     uint32 `json:"sentPrefixCounter"`
	} `json:"addressFamilyInfo"`
	BFD *struct {
		Status string `json:"status"`
	} `json:"peerBfdInfo"`
}

// ParsePeers reads the sessions of show bgp neighbors json, keyed by interface or address, into
// the peers of a router's status, by BGPPeer document and then by neighbour. A session of no
// BGPPeer's peer group is left out; FRR prints {} without a BGP instance.
func ParsePeers(data []byte) ([]*nodev1.BGPPeerStatus, error) {
	var neighbors map[string]neighbor
	if err := json.Unmarshal(data, &neighbors); err != nil {
		return nil, fmt.Errorf("read FRR's BGP neighbours: %w", err)
	}
	var peers []*nodev1.BGPPeerStatus
	for key, n := range neighbors {
		name, ok := strings.CutPrefix(n.PeerGroup, PeerGroupPrefix)
		if !ok {
			continue
		}
		p := &nodev1.BGPPeerStatus{Name: name, Neighbor: key, State: n.State}
		if n.State == "Established" {
			p.UptimeSeconds = n.UpMsec / 1000
		}
		for _, af := range n.AddressFamilyInfo {
			p.PrefixesReceived += af.Accepted
			p.PrefixesSent += af.Sent
		}
		if n.BFD != nil {
			p.Bfd = bfdState(n.BFD.Status)
		}
		peers = append(peers, p)
	}
	slices.SortFunc(peers, func(a, b *nodev1.BGPPeerStatus) int {
		return strings.Compare(a.Name+"\x00"+a.Neighbor, b.Name+"\x00"+b.Neighbor)
	})
	return peers, nil
}

// bfdState is FRR's BFD status as the status names it: up, down, init, admin-down or unknown.
func bfdState(s string) string {
	switch s := strings.ToLower(s); s {
	case "up", "down", "init":
		return s
	case "admindown", "admin-down", "adm-down":
		return "admin-down"
	}
	return "unknown"
}

// path is a path as show bgp <family> unicast detail json prints it.
type path struct {
	ASPath struct {
		String string `json:"string"`
	} `json:"aspath"`
	Metric    uint32  `json:"metric"`
	LocalPref *uint32 `json:"locPrf"`
	Valid     bool    `json:"valid"`
	Multipath bool    `json:"multipath"`
	Local     bool    `json:"local"`
	Best      *struct {
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

// ParseRoutes reads the paths of show bgp <family> unicast detail json into the routes BGP
// selected: the best path of each prefix and the equal paths installed beside it, by prefix.
// FRR prints a warning and no routes without a BGP instance.
func ParseRoutes(data []byte) ([]*nodev1.BGPRoute, error) {
	var table struct {
		Routes map[string][]path `json:"routes"`
	}
	if err := json.Unmarshal(data, &table); err != nil {
		return nil, fmt.Errorf("read FRR's BGP paths: %w", err)
	}
	var routes []*nodev1.BGPRoute
	for prefix, paths := range table.Routes {
		for _, p := range paths {
			best := p.Best != nil && p.Best.Overall
			if !p.Valid || !best && !p.Multipath {
				continue
			}
			r := &nodev1.BGPRoute{Prefix: prefix, Med: p.Metric, LocalPref: p.LocalPref, Best: best}
			if !p.Local {
				r.AsPath = p.ASPath.String
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
	slices.SortStableFunc(routes, func(a, b *nodev1.BGPRoute) int {
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
