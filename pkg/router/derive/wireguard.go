package derive

import (
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/trevex/chalkos/pkg/refusals"
)

// The WireGuard interfaces, each a link of its own whose units router-networkd renders: a .netdev
// and a .network, 00-wg-<name>, which gives the link its addresses and its VRF. The model takes
// the link as one networkd puts in the WireGuard's VRF by that unit, for the firewall's zones and
// BGP's sessions alike.

// WireGuardUnitPrefix starts the names of a WireGuard's units, so that its .network sorts before
// most of the node's own. A Network that networkd may pick for the link before it is refused.
const WireGuardUnitPrefix = "00-wg-"

// WireGuardInterface is a WireGuard document with its VRF and its peers, in the order of their
// names.
type WireGuardInterface struct {
	Name string
	Spec WireGuardSpec
	VRF  string
	// Table is the routing table of the WireGuard's VRF, the main table's 254 for the default VRF,
	// which networkd routes the peers' allowed IPs in.
	Table uint32
	Peers []Named[WireGuardPeerSpec]
}

// withWireGuards is m with each WireGuard's .network unit, which networkd picks for its link
// once WireGuardInterfaces refuses every Network it may pick before, and with the link as a
// member of the WireGuard's VRF unless that is the default one.
func (m Membership) withWireGuards(wgs []Named[WireGuardSpec]) Membership {
	if len(wgs) == 0 {
		return m
	}
	m.Units = slices.Clone(m.Units)
	m.Networks = maps.Clone(m.Networks)
	m.Members = slices.Clone(m.Members)
	for _, w := range wgs {
		unit := Unit{Name: WireGuardUnitPrefix + w.Name, Match: NameMatch{Words: []NameWord{{Glob: w.Name, Text: w.Name}}}}
		m.Units = append(m.Units, unit)
		if vrf := VRFOf(w.Spec.VRF); vrf != DefaultVRF {
			if m.Networks == nil {
				m.Networks = map[string]string{}
			}
			m.Networks[unit.Name] = vrf
			m.Members = append(m.Members, Member{VRF: vrf, Glob: w.Name, Text: w.Name, Network: unit.Name, Path: "spec.match"})
		}
	}
	slices.SortStableFunc(m.Units, func(a, b Unit) int { return strings.Compare(a.Name+".network", b.Name+".network") })
	return m
}

// WireGuardInterfaces are the WireGuard documents with their peers and the tables of their VRFs.
// It refuses, each fault once, a Network document of a WireGuard unit's name, one whose Name=
// names a WireGuard's link and one that networkd may pick for the link before that unit, a VRF's
// member or a Network's VRF= that puts the link in another VRF than the WireGuard's, and a peer's
// faults as checkWireGuardPeer finds them.
func (d Model) WireGuardInterfaces() ([]WireGuardInterface, error) {
	if len(d.WireGuards) == 0 && len(d.WireGuardPeers) == 0 {
		return nil, nil
	}
	units, err := d.Units()
	if err != nil {
		return nil, err
	}
	tables := map[string]uint32{DefaultVRF: mainTable}
	for _, v := range d.VRFs {
		tables[v.Name] = v.Spec.Table
	}
	var errs []error
	out := make([]WireGuardInterface, 0, len(d.WireGuards))
	index := map[string]int{}
	for _, w := range d.WireGuards {
		// The WireGuard's rules keep its name to 15 characters, an interface's.
		if !PlainInterface.MatchString(w.Name) {
			return nil, InternalErrorf("WireGuard %s: its name is no interface name of 15 characters", w.Name)
		}
		vrf, err := VRFName(w.Spec.VRF)
		if err != nil {
			return nil, Within("WireGuard "+w.Name, err)
		}
		own := WireGuardUnitPrefix + w.Name
		table, err := MustGet(tables, "VRF", vrf)
		if err != nil {
			return nil, Within("WireGuard "+w.Name, err)
		}
		// The Networks refused already, which the order of the units need not refuse again.
		named := map[string]bool{}
		for _, u := range units {
			switch {
			case u.Name == own:
				errs = append(errs, refusals.Errorf("Network", u.Name, "metadata.name", "Network %s takes the name of WireGuard %s's unit; rename the document", u.Name, w.Name))
				named[u.Name] = true
			case u.Device:
			case slices.ContainsFunc(u.Match.Positives(), func(n NameWord) bool { return n.Glob == w.Name }):
				errs = append(errs, refusals.Errorf("Network", u.Name, "spec.match", "Network %s names %s, the interface of WireGuard %s, whose own unit configures it; drop %s from the Network", u.Name, w.Name, w.Name, w.Name))
				named[u.Name] = true
			}
		}
		withOwn := append(slices.Clone(units), Unit{Name: own, Match: NameMatch{Words: []NameWord{{Glob: w.Name, Text: w.Name}}}})
		slices.SortStableFunc(withOwn, func(a, b Unit) int { return strings.Compare(a.Name+".network", b.Name+".network") })
		if u := Candidates(withOwn, w.Name)[0]; u.Name != own && !named[u.Name] {
			switch {
			case u.Device:
				errs = append(errs, refusals.Errorf("WireGuard", w.Name, "metadata.name", "WireGuard %s takes the name of VRF %s's device; give the WireGuard another name", w.Name, w.Name))
			case u.Narrowed:
				errs = append(errs, refusals.Errorf("Network", u.Name, "metadata.name", "Network %s may match the link of WireGuard %s before %s, should its other [Match] keys match it; rename it to sort after %s", u.Name, w.Name, own, own))
			default:
				errs = append(errs, refusals.Errorf("Network", u.Name, "metadata.name", "Network %s matches the link of WireGuard %s before %s; rename it to sort after %s", u.Name, w.Name, own, own))
			}
		}
		for _, m := range d.Membership.Members {
			if m.Network == own || m.VRF == vrf {
				continue
			}
			if ok, _ := path.Match(m.Glob, w.Name); !ok {
				continue
			}
			if m.Network == "" {
				errs = append(errs, refusals.Errorf("WireGuard", w.Name, "spec.vrf", "VRF %s holds %s by its %s, but the WireGuard is in VRF %s; give the WireGuard vrf %s, or keep its link out of VRF %s's interfaces", m.VRF, w.Name, m.Glob, vrf, m.VRF, m.VRF))
			} else {
				errs = append(errs, refusals.Errorf("WireGuard", w.Name, "spec.vrf", "Network %s gives VRF=%s to the links its Name=%s matches, %s among them, but the WireGuard is in VRF %s; a WireGuard's link takes the VRF of the WireGuard's vrf", m.Network, m.VRF, m.Glob, w.Name, vrf))
			}
		}
		index[w.Name] = len(out)
		out = append(out, WireGuardInterface{Name: w.Name, Spec: w.Spec, VRF: vrf, Table: table})
	}
	for _, p := range d.WireGuardPeers {
		i, ok := index[p.Spec.Interface]
		if !ok {
			return nil, InternalErrorf("WireGuardPeer %s: its interface %q is no WireGuard document", p.Name, p.Spec.Interface)
		}
		w := &out[i]
		if err := checkWireGuardPeer(w, p); err != nil {
			errs = append(errs, err)
		}
		w.Peers = append(w.Peers, p)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

// checkWireGuardPeer refuses the peer p at its first fault: an endpoint networkd cannot read, a
// default route among its allowed IPs, which networkd would route through the peer in the VRF's
// table, and, against the peers w holds so far, the public key of one of them or allowed IPs that
// overlap theirs. The kernel sends an address to one peer alone, the last configured, and keeps a
// key once.
func checkWireGuardPeer(w *WireGuardInterface, p Named[WireGuardPeerSpec]) error {
	if e := p.Spec.Endpoint; e != nil && !validEndpoint(*e) {
		return refusals.Errorf("WireGuardPeer", p.Name, "spec.endpoint", "endpoint %q is none of [<IPv6>]:<port>, <IPv4>:<port> and <host name>:<port> with a port from 1 to 65535", *e)
	}
	for j, a := range p.Spec.AllowedIPs {
		if a.Bits() == 0 {
			return refusals.Errorf("WireGuardPeer", p.Name, fmt.Sprintf("spec.allowedIPs[%d]", j), "allowedIPs %s is a default route, which the router does not send through a peer; name the ranges behind the peer", a)
		}
	}
	for _, o := range w.Peers {
		if o.Spec.PublicKey == p.Spec.PublicKey {
			return refusals.Errorf("WireGuardPeer", p.Name, "spec.publicKey", "WireGuardPeer %s has the public key of WireGuardPeer %s on %s", p.Name, o.Name, w.Name)
		}
	}
	for j, a := range p.Spec.AllowedIPs {
		for _, o := range w.Peers {
			for _, b := range o.Spec.AllowedIPs {
				if a.Masked().Overlaps(b.Masked()) {
					return refusals.Errorf("WireGuardPeer", p.Name, fmt.Sprintf("spec.allowedIPs[%d]", j), "allowedIPs %s overlaps %s of WireGuardPeer %s on %s; a WireGuard sends each address to one peer", a, b, o.Name, w.Name)
				}
			}
		}
	}
	return nil
}

// validEndpoint reports whether e is an endpoint networkd reads: an address and a port, the
// address of IPv6 in brackets, or a host name of RFC 1123 labels and a port, the port from 1 to
// 65535.
func validEndpoint(e string) bool {
	if ap, err := netip.ParseAddrPort(e); err == nil {
		return ap.Port() > 0 && ap.Addr().Zone() == ""
	}
	host, port, ok := strings.Cut(e, ":")
	if !ok || strings.Contains(port, ":") {
		return false
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 || port[0] == '0' {
		return false
	}
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if !hostLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// hostLabel is a label of a host name, as RFC 1123 gives them: letters, digits and inner hyphens,
// 1 to 63 of them.
var hostLabel = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9]{0,61}[A-Za-z0-9])?$`)
