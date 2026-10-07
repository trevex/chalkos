// Package nodeip picks the address a Kubernetes node registers with and its control plane
// advertises: a fixed address once an interface holds it, or else the first of the node's
// addresses that a filter of subnets selects.
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

// Selector picks the node's address among the addresses of its interfaces.
type Selector struct {
	// Fixed is the address set for the node. When valid, it is the only address picked, once an
	// interface holds it.
	Fixed netip.Addr
	// Filter selects among the node's addresses when no address is fixed.
	Filter Filter
	// Reserved are ranges that never hold the node's address: the pod and service ranges.
	Reserved []netip.Prefix
}

func (s Selector) String() string {
	if s.Fixed.IsValid() {
		return "nodeIP " + s.Fixed.String()
	}
	return s.Filter.String()
}

// Select returns the fixed address once an interface holds it. Otherwise it returns the first
// global unicast address outside the reserved ranges and the interfaces of Kubernetes that the
// filter matches: IPv4 addresses before IPv6 ones, then by interface name and by address, so the
// choice does not depend on the order the kernel lists them in.
func (s Selector) Select(addrs []Address) (netip.Addr, error) {
	sorted := make([]Address, 0, len(addrs))
	for _, a := range addrs {
		sorted = append(sorted, Address{Interface: a.Interface, IP: a.IP.Unmap().WithZone("")})
	}
	slices.SortFunc(sorted, compare)
	var seen []Address
	for _, a := range sorted {
		if s.Fixed.IsValid() && a.IP == s.Fixed.Unmap() {
			return a.IP, nil
		}
		if !a.IP.IsGlobalUnicast() {
			continue
		}
		seen = append(seen, a)
		if !s.Fixed.IsValid() && s.eligible(a) && s.Filter.Matches(a.IP) {
			return a.IP, nil
		}
	}
	return netip.Addr{}, &NoMatchError{Selector: s.String(), Addresses: seen}
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

// NoMatchError means no address of the node matches the selector. Addresses are the node's
// global unicast addresses, in the order Select considered them.
type NoMatchError struct {
	Selector  string
	Addresses []Address
}

func (e *NoMatchError) Error() string {
	have := "the node has no addresses"
	if len(e.Addresses) > 0 {
		list := make([]string, len(e.Addresses))
		for i, a := range e.Addresses {
			list[i] = a.String()
		}
		have = "the node has " + strings.Join(list, ", ")
	}
	return fmt.Sprintf("no node address matches %s (%s)", e.Selector, have)
}

// Waiter waits for the node's address: a DHCP lease or an address a routing daemon adds may
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

// Wait returns the address s selects as soon as there is one. It looks at least once, and
// returns the last error once timeout passed.
func (w Waiter) Wait(s Selector, timeout time.Duration) (netip.Addr, error) {
	deadline := w.Now().Add(timeout)
	for {
		addrs, err := w.Addresses()
		if err == nil {
			var ip netip.Addr
			if ip, err = s.Select(addrs); err == nil {
				return ip, nil
			}
		}
		remaining := deadline.Sub(w.Now())
		if remaining <= 0 {
			return netip.Addr{}, err
		}
		w.Sleep(min(w.Interval, remaining))
	}
}
