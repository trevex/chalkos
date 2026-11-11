package vip

import (
	"errors"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
)

// addresses are the cluster's VIPs on this node: each on the interface the cluster names, or
// else, in l2 mode, on the one holding the node's address of its family. In l2 mode the holder
// announces them to its neighbours; in routed mode a routing daemon on the node announces them.
type addresses struct {
	addrs []Address
	// add, announce and remove change the node's interfaces and tell its neighbours; tests
	// replace them.
	add      func(Address) (bool, error)
	announce func(Address) error
	remove   func(Address) error
	// announcements is how often an added address is announced, spacing apart, so neighbours
	// that missed the first announcement still learn of it. A routed VIP is announced none.
	announcements int
	spacing       time.Duration

	mu sync.Mutex
	// releases counts the releases, after which earlier announcements stop.
	releases int
}

func newAddresses(addrs []Address, announcements int) *addresses {
	return &addresses{addrs: addrs, add: Add, announce: Announce, remove: Remove, announcements: announcements, spacing: time.Second}
}

// Acquire adds the addresses that are missing and announces those alone, if the holder announces
// any: an address that was there already was announced when it was added.
func (a *addresses) Acquire() error {
	var errs []error
	var added []Address
	for _, addr := range a.addrs {
		ok, err := a.add(addr)
		if err != nil {
			errs = append(errs, err)
		} else if ok {
			added = append(added, addr)
		}
	}
	if len(added) == 0 || a.announcements == 0 {
		return errors.Join(errs...)
	}
	a.mu.Lock()
	releases := a.releases
	errs = append(errs, a.announceLocked(added))
	a.mu.Unlock()
	go func() {
		for range a.announcements - 1 {
			time.Sleep(a.spacing)
			a.mu.Lock()
			if a.releases != releases {
				a.mu.Unlock()
				return
			}
			err := a.announceLocked(added)
			a.mu.Unlock()
			if err != nil {
				log.Printf("kubernetes: %v", err)
			}
		}
	}()
	return errors.Join(errs...)
}

func (a *addresses) announceLocked(addrs []Address) error {
	var errs []error
	for _, addr := range addrs {
		errs = append(errs, a.announce(addr))
	}
	return errors.Join(errs...)
}

// Release stops the announcements and removes the addresses.
func (a *addresses) Release() error {
	a.mu.Lock()
	a.releases++
	a.mu.Unlock()
	var errs []error
	for _, addr := range a.addrs {
		errs = append(errs, a.remove(addr))
	}
	return errors.Join(errs...)
}

// NodeAddresses finds the interfaces of the cluster's VIPs on this node. A routed VIP goes on the
// interface the node names, which its routing daemon announces, and the holder announces nothing
// on the link; an l2 VIP is announced three times, a second apart.
func NodeAddresses(c k8s.Cluster, s knode.State) (Holder, error) {
	vips := c.VIPAddresses()
	announcements := 3
	if c.VIP.Mode == k8s.VIPModeRouted {
		if c.VIP.Interface == "" {
			return nil, errors.New("the Cluster's VIP is routed, but the node names no vip.interface to add it to")
		}
		announcements = 0
	}
	var held []Address
	if c.VIP.Interface != "" {
		for _, ip := range vips {
			held = append(held, Address{IP: ip, Interface: c.VIP.Interface})
		}
		return newAddresses(held, announcements), nil
	}
	ips, err := s.ReadNodeIPs()
	if err != nil {
		return nil, err
	}
	system, err := nodeip.SystemAddresses()
	if err != nil {
		return nil, err
	}
	for _, ip := range vips {
		i := slices.IndexFunc(system, func(a nodeip.Address) bool {
			if nodeip.FamilyOf(a.IP) != nodeip.FamilyOf(ip) {
				return false
			}
			return slices.Contains(ips, a.IP)
		})
		if i < 0 {
			return nil, fmt.Errorf("the node has no %s address for the VIP %s", nodeip.FamilyOf(ip), ip)
		}
		held = append(held, Address{IP: ip, Interface: system[i].Interface})
	}
	return newAddresses(held, announcements), nil
}
