package node

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	"github.com/trevex/chalkos/pkg/kubernetes/vxlan"
)

// addressesTarget is the systemd target that says the node's addresses have settled.
const addressesTarget = "chalkos-node-addresses.target"

// Resolver waits up to timeout for the addresses the selector picks.
type Resolver func(sel nodeip.Selector, timeout time.Duration) ([]nodeip.Address, error)

// WaitForAddresses waits for the addresses the selector picks among the node's own.
func WaitForAddresses(sel nodeip.Selector, timeout time.Duration) ([]nodeip.Address, error) {
	log.Printf("picking the node's addresses, %s, waiting up to %v", sel, timeout)
	picked, err := nodeip.NewWaiter().Wait(sel, timeout)
	if err != nil {
		return nil, err
	}
	log.Printf("the node's addresses are %v", picked)
	return picked, nil
}

// pickAddresses waits for the node's addresses and checks that flannel's VXLAN can use them.
func pickAddresses(p Paths, c kubernetes.Cluster, n kubernetes.Node, resolve Resolver) ([]nodeip.Address, error) {
	sel, err := nodeSelector(p, c, n)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(c.NodeIP.Timeout) * time.Second
	picked, err := resolve(sel, timeout)
	if err != nil {
		// Units that add the node's addresses, such as a routing daemon's, order themselves before
		// the target, which the preparation runs after. The resolver returns an error only once
		// its deadline passed (nodeip.Waiter.Wait), so the whole timeout went by.
		err = fmt.Errorf("%w, %v after %s", err, timeout, addressesTarget)
	}
	if pinned := (*nodeip.PinnedError)(nil); errors.As(err, &pinned) {
		return nil, fmt.Errorf("%w; restore it, or remove the node's etcd member with chalkctl etcd remove-member %s and reinstall the node", err, n.Name)
	}
	if err != nil {
		return nil, err
	}
	if err := checkFlannelLinks(c, picked); err != nil {
		return nil, err
	}
	if c.Flannel != nil {
		if err := checkVXLANSources(picked, c.VXLANSources()); err != nil {
			return nil, err
		}
	}
	return picked, nil
}

// checkFlannelLinks refuses node addresses on interfaces flannel's VXLAN cannot use as they are:
//   - flannel takes its MTU from the interface holding the node's address unless one is set,
//     which on a loopback interface is far beyond any network's;
//   - flannel gives its VXLAN devices that MTU minus 50, and the kernel limits them to the
//     interface's MTU minus the encapsulation, 50 for IPv4 and 70 for IPv6, without telling
//     flannel, so a dummy interface needs room for the MTU set.
func checkFlannelLinks(c kubernetes.Cluster, picked []nodeip.Address) error {
	if c.Flannel == nil {
		return nil
	}
	for _, a := range picked {
		switch {
		case a.Link.Loopback && c.Flannel.MTU == 0:
			return fmt.Errorf("the node's address %s is on the loopback interface %s, whose MTU flannel would take; set the Flannel document's spec.mtu", a.IP, a.Interface)
		case a.Link.Kind == "dummy" && c.Flannel.MTU > 0:
			need := c.Flannel.MTU
			if a.IP.Is6() {
				need += 20
			}
			if a.Link.MTU < need {
				return fmt.Errorf("the node's address %s is on the dummy interface %s with MTU %d; flannel's %s VXLAN with the Flannel document's MTU %d needs an MTU of at least %d there", a.IP, a.Interface, a.Link.MTU, nodeip.FamilyOf(a.IP), c.Flannel.MTU, need)
			}
		}
	}
	return nil
}

// ReadNodeIPs reads the addresses Prepare picked, the primary family's first.
func ReadNodeIPs(p Paths) ([]netip.Addr, error) {
	return readAddresses(p.NodeIP())
}

// WritePin pins the node to the addresses, the ones the preparation picked and etcd advertises.
func WritePin(p Paths, ips []netip.Addr) error {
	if len(ips) == 0 {
		return errors.New("pin the node's addresses: there are none")
	}
	return atomicfile.WriteFile(p.Pin(), nodeIPFile(ips), 0o644)
}

// ReadPin returns the addresses the node is pinned to, nil when it is not pinned.
func ReadPin(p Paths) ([]netip.Addr, error) {
	pin, err := readAddresses(p.Pin())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return pin, err
}

// readAddresses reads a file of addresses, one per line, as nodeIPFile writes it. IPv4-mapped
// addresses are returned in their IPv4 form. A file without an address is refused.
func readAddresses(path string) ([]netip.Addr, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	for _, line := range strings.Fields(string(data)) {
		ip, err := netip.ParseAddr(line)
		if err != nil || ip.Zone() != "" {
			return nil, fmt.Errorf("%s holds %q, which is not an address", path, line)
		}
		ips = append(ips, ip.Unmap())
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%s holds no address", path)
	}
	return ips, nil
}

// nodeIPFile is the content of the file holding the addresses: one per line.
func nodeIPFile(ips []netip.Addr) []byte {
	var b strings.Builder
	for _, ip := range ips {
		b.WriteString(ip.String() + "\n")
	}
	return []byte(b.String())
}

// nodeSelector is how the node picks its addresses: a control-plane node that is pinned waits
// for exactly its pinned addresses, any other node selects them as its cluster and identity say.
func nodeSelector(p Paths, c kubernetes.Cluster, n kubernetes.Node) (nodeip.Selector, error) {
	if c.Kind == kubernetes.KindControlPlane {
		pin, err := ReadPin(p)
		if err != nil {
			return nodeip.Selector{}, err
		}
		if pin != nil {
			sel := nodeip.Selector{Fixed: pin, Pinned: true, SameInterface: c.Flannel != nil && len(pin) > 1}
			for _, ip := range pin {
				sel.Families = append(sel.Families, nodeip.FamilyOf(ip))
			}
			// The pin holds an address of each family the cluster was created with, the primary
			// one first. Other families, or another order, would change the cluster's addressing
			// and the addresses etcd's peers know the node by.
			if families := c.Families(); !slices.Equal(sel.Families, families) {
				return nodeip.Selector{}, fmt.Errorf("the cluster was created with families %v; ipFamilies is %v; changing the families of a running cluster is not supported",
					sel.Families, families)
			}
			return sel, nil
		}
	}
	return c.NodeIPSelector(n)
}

// checkVXLANSources refuses picked addresses outside the ranges the other nodes take VXLAN from.
// The node's config was checked against them already, but an address picked from the subnets at
// boot is known only now.
func checkVXLANSources(picked []nodeip.Address, sources []netip.Prefix) error {
	addrs := make([]netip.Addr, len(picked))
	for i, a := range picked {
		addrs[i] = a.IP
	}
	return kubernetes.CheckVXLANSources(sources, addrs)
}

// vxlanAccept is what the firewall's VXLAN rule accepts for the node's addresses.
func vxlanAccept(picked []nodeip.Address, sources []netip.Prefix) vxlan.Accept {
	a := vxlan.Accept{Sources: sources}
	for _, p := range picked {
		a.Destinations = append(a.Destinations, vxlan.DestinationOf(p))
	}
	return a
}

// Firewall accepts VXLAN as p.VXLAN() says, or none when the file does not exist.
type Firewall func(p Paths) error

// VXLANRule is the Firewall that fills the table chalkos-vxlan from p.VXLAN() with rule, which
// prints the table to the journal; the error points there.
func VXLANRule(rule vxlan.Rule) Firewall {
	return func(p Paths) error {
		if err := rule.Apply(p.VXLAN()); err != nil {
			name := "<node>"
			if n, rerr := kubernetes.ReadNode(p.NodeFile); rerr == nil {
				name = n.Name
			}
			return fmt.Errorf("accept VXLAN to the node's address: %w; see chalkctl logs %s --unit chalkos-kubernetes", err, name)
		}
		return nil
	}
}
