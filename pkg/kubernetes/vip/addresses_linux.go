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
// else on the one holding the node's address of its family.
type addresses struct {
	addrs []Address
	// add, announce and remove change the node's interfaces and tell its neighbours; tests
	// replace them.
	add      func(Address) (bool, error)
	announce func(Address) error
	remove   func(Address) error
	// announcements is how often an added address is announced, spacing apart, so neighbours
	// that missed the first announcement still learn of it.
	announcements int
	spacing       time.Duration

	mu sync.Mutex
	// releases counts the releases, after which earlier announcements stop.
	releases int
}

func newAddresses(addrs []Address) *addresses {
	return &addresses{addrs: addrs, add: Add, announce: Announce, remove: Remove, announcements: 3, spacing: time.Second}
}

// Acquire adds the addresses that are missing and announces those alone: an address that was
// there already was announced when it was added.
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
	if len(added) == 0 {
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

// NodeAddresses finds the interfaces of the cluster's VIPs on this node.
func NodeAddresses(c k8s.Cluster, p knode.Paths) (Holder, error) {
	vips := c.VIPAddresses()
	var held []Address
	if c.VIP.Interface != "" {
		for _, ip := range vips {
			held = append(held, Address{IP: ip, Interface: c.VIP.Interface})
		}
		return newAddresses(held), nil
	}
	ips, err := knode.ReadNodeIPs(p)
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
	return newAddresses(held), nil
}
