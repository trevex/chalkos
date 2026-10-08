// Package nodeip picks the addresses a Kubernetes node registers with and its control plane
// advertises, one of each address family: a fixed address once an interface holds it, or else
// the first of the node's addresses of the family that a filter of subnets selects.
package nodeip

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// Address is an address an interface of the node holds.
type Address struct {
	Interface string
	IP        netip.Addr
	// Link is the interface holding the address, as SystemAddresses finds it.
	Link Link
}

// Link is an interface of the node.
type Link struct {
	Name string
	// Loopback is set on loopback interfaces, such as lo.
	Loopback bool
	// Kind is the kind of a virtual interface as netlink reports it, such as dummy, vlan or
	// bridge; empty for a physical one.
	Kind string
	MTU  int
}

func (a Address) String() string { return a.IP.String() + " on " + a.Interface }

// kubernetesInterfaces are name prefixes of the interfaces the pod network and kube-proxy
// create; their addresses are never the node's.
var kubernetesInterfaces = []string{"flannel.", "cni", "veth", "kube-"}

// Filter selects addresses by subnet: an address matches when an included subnet holds it, or
// no subnet is included, and no excluded subnet holds it.
type Filter struct {
	subnets          []string
	include, exclude []netip.Prefix
}

// ParseFilter reads subnets in CIDR notation; a leading "!" excludes a subnet.
func ParseFilter(subnets []string) (Filter, error) {
	f := Filter{subnets: slices.Clone(subnets)}
	for _, s := range subnets {
		text, exclude := strings.CutPrefix(s, "!")
		prefix, err := netip.ParsePrefix(text)
		if err != nil {
			return Filter{}, fmt.Errorf("validSubnets: %q is not a subnet in CIDR notation", s)
		}
		// Select compares addresses in their IPv4 form, which such a subnet never holds.
		if prefix.Addr().Is4In6() {
			return Filter{}, fmt.Errorf("validSubnets: %q is an IPv4-mapped IPv6 subnet; write the IPv4 form, such as 10.0.0.0/8", s)
		}
		if exclude {
			f.exclude = append(f.exclude, prefix.Masked())
		} else {
			f.include = append(f.include, prefix.Masked())
		}
	}
	return f, nil
}

// Matches reports whether the filter selects ip.
func (f Filter) Matches(ip netip.Addr) bool {
	included := len(f.include) == 0
	for _, p := range f.include {
		included = included || p.Contains(ip)
	}
	if !included {
		return false
	}
	for _, p := range f.exclude {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

func (f Filter) String() string {
	if len(f.subnets) == 0 {
		return "the default filter"
	}
	return "validSubnets " + strings.Join(f.subnets, ", ")
}

// Family is an IP address family.
type Family string

// The address families a node has addresses of.
const (
	IPv4 Family = "ipv4"
	IPv6 Family = "ipv6"
)

// ParseFamily reads "ipv4" or "ipv6".
func ParseFamily(s string) (Family, error) {
	switch f := Family(s); f {
	case IPv4, IPv6:
		return f, nil
	}
	return "", fmt.Errorf("%q is not an address family; use ipv4 or ipv6", s)
}

// FamilyOf returns the family of ip.
func FamilyOf(ip netip.Addr) Family {
	if ip.Unmap().Is4() {
		return IPv4
	}
	return IPv6
}

// Selector picks the node's addresses, one of each family, among the addresses of its
// interfaces.
type Selector struct {
	// Families are the families the node has an address of, the primary one first. Empty means
	// IPv4 alone.
	Families []Family
	// Fixed are addresses set for the node, at most one per family. A family with a fixed
	// address picks only that address, once an interface holds it.
	Fixed []netip.Addr
	// Pinned marks Fixed as the addresses the node was pinned to when it became an etcd member.
	Pinned bool
	// Filter selects among the node's addresses of the families without a fixed address.
	Filter Filter
	// Reserved are ranges that never hold the node's addresses: the pod and service ranges.
	Reserved []netip.Prefix
	// Last are addresses picked only when no other address of their family matches: the API
	// server endpoint's and the virtual IPs, which move between control-plane nodes.
	Last []netip.Addr
	// SameInterface picks the addresses of all families on one interface, as flannel needs
	// them: the first address of the primary family that has an address of every other family
	// on its interface, with those.
	SameInterface bool
}

// families returns the families in order, IPv4 alone when none are listed.
func (s Selector) families() []Family {
	if len(s.Families) == 0 {
		return []Family{IPv4}
	}
	return s.Families
}

// fixed returns the fixed address of the family, if any.
func (s Selector) fixed(f Family) (netip.Addr, bool) {
	for _, ip := range s.Fixed {
		if FamilyOf(ip) == f {
			return ip.Unmap(), true
		}
	}
	return netip.Addr{}, false
}

// describe says how the selector picks the address of a family.
func (s Selector) describe(f Family) string {
	ip, ok := s.fixed(f)
	switch {
	case ok && s.Pinned:
		return "the pinned address " + ip.String()
	case ok:
		return "nodeIP " + ip.String()
	}
	return s.Filter.String()
}

func (s Selector) String() string {
	var parts []string
	for _, f := range s.families() {
		parts = append(parts, string(f)+" by "+s.describe(f))
	}
	return strings.Join(parts, ", ")
}

// Select returns one address of each family, in the order of Families. A family's fixed address
// is taken once an interface holds it. Otherwise it is the first global unicast address of the
// family outside the reserved ranges and the interfaces of Kubernetes that the filter matches,
// by interface name and by address, so the choice does not depend on the order the kernel lists
// them in, and the Last addresses after all others. With SameInterface the addresses are on one
// interface, or Select fails with an *InterfaceError.
func (s Selector) Select(addrs []Address) ([]Address, error) {
	sorted := make([]Address, 0, len(addrs))
	for _, a := range addrs {
		a.IP = a.IP.Unmap().WithZone("")
		sorted = append(sorted, a)
	}
	slices.SortFunc(sorted, compare)
	last := func(ip netip.Addr) bool {
		return slices.ContainsFunc(s.Last, func(l netip.Addr) bool { return l.Unmap().WithZone("") == ip })
	}
	slices.SortStableFunc(sorted, func(a, b Address) int {
		switch {
		case last(a.IP) && !last(b.IP):
			return 1
		case !last(a.IP) && last(b.IP):
			return -1
		}
		return 0
	})
	var picked []Address
	var candidates [][]Address
	for _, f := range s.families() {
		c, err := s.candidates(f, sorted)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
		picked = append(picked, c[0])
	}
	if !s.SameInterface || len(picked) < 2 {
		return picked, nil
	}
	for _, first := range candidates[0] {
		pair := []Address{first}
		for _, other := range candidates[1:] {
			if i := slices.IndexFunc(other, func(a Address) bool { return a.Interface == first.Interface }); i >= 0 {
				pair = append(pair, other[i])
			}
		}
		if len(pair) == len(picked) {
			return pair, nil
		}
	}
	return nil, &InterfaceError{Addresses: picked}
}

// candidates returns the addresses of family f the selector may pick, in order: the fixed
// address, or the eligible ones the filter matches.
func (s Selector) candidates(f Family, sorted []Address) ([]Address, error) {
	fixed, isFixed := s.fixed(f)
	var seen, matching []Address
	for _, a := range sorted {
		if FamilyOf(a.IP) != f {
			continue
		}
		if isFixed && a.IP == fixed {
			matching = append(matching, a)
			continue
		}
		if !a.IP.IsGlobalUnicast() {
			continue
		}
		seen = append(seen, a)
		if !isFixed && s.eligible(a) && s.Filter.Matches(a.IP) {
			matching = append(matching, a)
		}
	}
	switch {
	case len(matching) > 0:
		return matching, nil
	case isFixed && s.Pinned:
		return nil, &PinnedError{Address: fixed}
	}
	return nil, &NoMatchError{Family: f, Selector: s.describe(f), Addresses: seen}
}

func (s Selector) eligible(a Address) bool {
	for _, prefix := range kubernetesInterfaces {
		if strings.HasPrefix(a.Interface, prefix) {
			return false
		}
	}
	for _, p := range s.Reserved {
		if p.Contains(a.IP) {
			return false
		}
	}
	return true
}

func compare(a, b Address) int {
	if a.IP.Is4() != b.IP.Is4() {
		if a.IP.Is4() {
			return -1
		}
		return 1
	}
	if c := strings.Compare(a.Interface, b.Interface); c != 0 {
		return c
	}
	return a.IP.Compare(b.IP)
}

// NoMatchError means no address of the node of a family matches the selector. Addresses are the
// node's global unicast addresses of that family, in the order Select considered them.
type NoMatchError struct {
	Family    Family
	Selector  string
	Addresses []Address
}

func (e *NoMatchError) Error() string {
	have := "the node has no " + string(e.Family) + " addresses"
	if len(e.Addresses) > 0 {
		list := make([]string, len(e.Addresses))
		for i, a := range e.Addresses {
			list[i] = a.String()
		}
		have = "the node has " + strings.Join(list, ", ")
	}
	return fmt.Sprintf("no %s node address matches %s (%s)", e.Family, e.Selector, have)
}

// InterfaceError means the node has no addresses of all families on one interface. Addresses
// are the ones picked without that condition.
type InterfaceError struct {
	Addresses []Address
}

func (e *InterfaceError) Error() string {
	where := make([]string, len(e.Addresses))
	for i, a := range e.Addresses {
		on := " on "
		if i == 0 {
			on = " is on "
		}
		where[i] = a.IP.String() + on + a.Interface
	}
	return "flannel needs the node's IPv4 and IPv6 addresses on one interface; " + strings.Join(where, ", ")
}

// PinnedError means an address the node was pinned to is on none of its interfaces.
type PinnedError struct {
	Address netip.Addr
}

func (e *PinnedError) Error() string {
	return fmt.Sprintf("pinned address %s is not present", e.Address)
}

// Waiter waits for the node's addresses: a DHCP lease or an address a routing daemon adds may
// arrive after the node started.
type Waiter struct {
	// Addresses lists the node's addresses.
	Addresses func() ([]Address, error)
	// Now and Sleep are the clock.
	Now   func() time.Time
	Sleep func(time.Duration)
	// Interval is the time between two looks at the node's addresses.
	Interval time.Duration
}

// NewWaiter returns a Waiter that looks at the system's addresses every second.
func NewWaiter() Waiter {
	return Waiter{Addresses: SystemAddresses, Now: time.Now, Sleep: time.Sleep, Interval: time.Second}
}

// Wait returns the addresses s selects as soon as there are all of them. It looks at least
// once, and returns the last error once timeout passed.
func (w Waiter) Wait(s Selector, timeout time.Duration) ([]Address, error) {
	deadline := w.Now().Add(timeout)
	for {
		addrs, err := w.Addresses()
		if err == nil {
			var picked []Address
			if picked, err = s.Select(addrs); err == nil {
				return picked, nil
			}
		}
		remaining := deadline.Sub(w.Now())
		if remaining <= 0 {
			return nil, err
		}
		w.Sleep(min(w.Interval, remaining))
	}
}
