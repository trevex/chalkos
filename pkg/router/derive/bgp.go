package derive

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"

	"github.com/trevex/chalkos/pkg/refusals"
)

// Session is a BGP session of a BGPPeer document: its neighbour as bgpd names it, the peer's
// address or, for a session on a link, the link's name.
type Session struct {
	Neighbour string
	OnLink    bool
}

// Sessions are the sessions of each BGPPeer document, by its name: its address, its interface,
// which is a link of its VRF, or the links of its VRF its interfaces name, globs matching the
// links the Network documents name. A glob skips the links of other VRFs, those of no VRF for a
// peer of a VRF and those of a VRF for a peer of the default one. It refuses, each peer at its
// first fault, an interface or glob of a VRF's device, an interface of a link of another VRF, a
// glob that matches links of other VRFs alone and a link a glob expands to that is named like an
// address. Of the peers it accepts, it then refuses a session that two peers of a VRF name. It
// guards, with an internal error, what frr.conf and the firewall write unquoted: an address
// PeerAddress guards and an interface or glob that is none.
func (d Model) Sessions() (map[string][]Session, error) {
	out := map[string][]Session{}
	devices := d.Devices()
	var errs []error
	for _, p := range d.Peers {
		ss, err := d.sessions(p, devices)
		if isInternal(err) {
			return nil, Within("BGPPeer "+p.Name, err)
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out[p.Name] = ss
	}
	// The peer that names each session of a VRF, so two peers never name one.
	owner := map[string]string{}
	for _, p := range d.Peers {
		vrf := VRFOf(p.Spec.VRF)
		for _, s := range out[p.Name] {
			key := vrf + " " + s.Neighbour
			if other, ok := owner[key]; ok {
				errs = append(errs, refusals.Errorf("BGPPeer", p.Name, peerField(p.Spec), "BGPPeer %s and BGPPeer %s both name %s in the VRF %s; give each session one peer", other, p.Name, s.Neighbour, vrf))
				continue
			}
			owner[key] = p.Name
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

// sessions are the sessions of the peer p, whose interfaces devices, the VRFs' devices, may not
// match: bgpd runs no session on the device, and the input hook sees it as the iifname of every
// packet from the VRF's links, so router-nftables would open the peer's ports on all of them.
func (d Model) sessions(p Named[PeerSpec], devices []string) ([]Session, error) {
	s, vrf := p.Spec, VRFOf(p.Spec.VRF)
	switch {
	case s.Address != nil:
		a, err := PeerAddress(*s.Address)
		if err != nil {
			return nil, err
		}
		return []Session{{Neighbour: a.String()}}, nil
	case s.Interface != nil:
		if !PlainInterface.MatchString(*s.Interface) {
			return nil, InternalErrorf("interface %q is no interface name", *s.Interface)
		}
		if dev, ok := DeviceMatch(*s.Interface, devices); ok {
			return nil, refusals.Errorf("BGPPeer", p.Name, "spec.interface", "interface %s is the device of VRF %s", dev, dev)
		}
		// The BGPPeer's rules refuse an interface named like an address; the VRF of its link is
		// the model's to find, as networkd puts it.
		linkIn, err := d.Membership.LinkVRF(*s.Interface)
		if err != nil {
			return nil, err
		}
		if linkIn != vrf {
			return nil, refusals.Errorf("BGPPeer", p.Name, "spec.interface", "interface %s is in the VRF %s, not in the peer's VRF %s", *s.Interface, linkIn, vrf)
		}
		return []Session{{Neighbour: *s.Interface, OnLink: true}}, nil
	}
	if len(s.Interfaces) == 0 {
		return nil, InternalErrorf("interfaces names no interface")
	}
	for i, glob := range s.Interfaces {
		if !InterfaceName.MatchString(glob) {
			return nil, InternalErrorf("interfaces: %q is no interface name or glob", glob)
		}
		if dev, ok := DeviceMatch(glob, devices); ok {
			return nil, refusals.Errorf("BGPPeer", p.Name, fmt.Sprintf("spec.interfaces[%d]", i), "%s matches %s, the device of VRF %s", glob, dev, dev)
		}
	}
	var out []Session
	// own and other count the links of each glob in the peer's VRF and in other VRFs.
	own, other := map[string]int{}, map[string]int{}
	for _, link := range d.Links {
		var globs []string
		for _, glob := range s.Interfaces {
			if MatchesGlob(glob, link) {
				globs = append(globs, glob)
			}
		}
		if len(globs) == 0 {
			continue
		}
		linkIn, err := d.Membership.LinkVRF(link)
		if err != nil {
			return nil, err
		}
		count := own
		if linkIn != vrf {
			count = other
		}
		for _, glob := range globs {
			count[glob]++
		}
		if linkIn != vrf {
			continue
		}
		// bgpd names a session on a link after its interface, and a line naming a link named like
		// an address would name either. The BGPPeer's rules refuse an interface named like an
		// address that the peer writes out; a glob's links are the model's to see.
		if _, err := netip.ParseAddr(link); err == nil {
			return nil, refusals.Errorf("BGPPeer", p.Name, fmt.Sprintf("spec.interfaces[%d]", slices.Index(s.Interfaces, globs[0])), "interface %q is an address, which FRR cannot tell from a neighbour's", link)
		}
		out = append(out, Session{Neighbour: link, OnLink: true})
	}
	for i, glob := range s.Interfaces {
		if own[glob] == 0 && other[glob] > 0 {
			return nil, refusals.Errorf("BGPPeer", p.Name, fmt.Sprintf("spec.interfaces[%d]", i), "%s matches no link of VRF %s, only links of other VRFs", glob, vrf)
		}
	}
	return out, nil
}

// peerField is the field of a BGPPeer that names its sessions, which a refusal of a session
// names: one of address, interface and interfaces, as the BGPPeer's rules require.
func peerField(s PeerSpec) string {
	switch {
	case s.Address != nil:
		return "spec.address"
	case s.Interface != nil:
		return "spec.interface"
	}
	return "spec.interfaces"
}

// PeerAddress parses a peer's address, which the BGPPeer's rule makes a unicast address beyond
// the router. frr.conf and the ruleset write it unquoted, so it guards a value that is no address,
// and one with a zone, which netip takes with any characters and the schema's format refuses.
func PeerAddress(s string) (netip.Addr, error) {
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return netip.Addr{}, InternalErrorf("address %q is no address without a zone", s)
	}
	return a, nil
}
