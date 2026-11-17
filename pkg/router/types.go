package router

import "regexp"

// ShortName matches a VRF or NAT64 name: at most 15 characters and safe on jool's and vtysh's
// command lines. modules/node/frr/diagnostics.nix and modules/node/router/nat64/default.nix repeat
// the regex; change them together.
var ShortName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,13}[a-z0-9])?$`)

// KernelRoute is a route in the kernel's routing tables.
type KernelRoute struct {
	Destination string
	// Type and Protocol are the kernel's names, or numbers it has no name for.
	Type, Protocol string
	Table, Metric  uint32
	NextHops       []*NextHop
}

// NextHop is a next hop of a kernel route.
type NextHop struct {
	// Gateway is empty for a route on the link alone.
	Gateway, Interface string
	// Weight is the next hop's weight among several, from 1; 0 for a route of one next hop.
	Weight uint32
}

// BGPRoute is a path BGP selected and installs for a prefix.
type BGPRoute struct {
	Prefix string
	// ASPath is the path's ASes separated by spaces, and Peer the neighbour it came from; both
	// empty for a route the router originates.
	ASPath, Peer string
	NextHops     []string
	// LocalPref is nil for a path without one.
	LocalPref   *uint32
	MED         uint32
	Communities []string
	// Best is set for the best path; otherwise the path is one of the equal paths installed beside it.
	Best bool
	// ImportedFrom is the VRF whose instance the path came from, for a path the instance imports;
	// empty for a path of its own peers or networks.
	ImportedFrom string
}
