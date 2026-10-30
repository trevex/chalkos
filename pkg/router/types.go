package router

import "regexp"

// ShortName is the name of a document whose name names a device or an instance in the kernel, of
// 15 characters at most, as a NAT64 or VRF document's. A name that matches it may reach jool's
// and vtysh's command lines. The vrf pattern of the router diagnostics in
// modules/node/router/diagnostics.nix holds the same regex; change both together.
var ShortName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,13}[a-z0-9])?$`)

// Peer is one BGP session of the router: a BGPPeer's address, or one of its interfaces, as FRR
// reports it.
type Peer struct {
	// VRF is the VRF the session runs in: a VRF document's name, or DefaultVRF.
	VRF string
	// Name is the BGPPeer document's, and Neighbor the session's interface or address.
	Name, Neighbor string
	// State is the session's BGP state, such as Established.
	State string
	// UptimeSeconds is how long the session has been established; 0 while it is not.
	UptimeSeconds uint64
	// PrefixesReceived and PrefixesSent count the routes of every family.
	PrefixesReceived, PrefixesSent uint32
	// BFD is the session's BFD state: up, down, admin-down or unknown; "" without bfd.
	BFD string
}

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
