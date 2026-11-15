package derive

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/refusals"
)

// The firewall's view of the router's links. The input and forward hooks see a packet from a VRF's
// link with the VRF's device as iifname; prerouting and postrouting see the links themselves. A
// NAT64's link n64-<name> carries its translated traffic, which the firewall treats apart. The
// FirewallZone, NATRule and PolicyRoute rules see neither the Networks' VRF devices nor the links
// the Network documents name, so the model checks the interfaces against them.

// checkFirewall refuses what router-nftables and the node API would apply otherwise than the
// documents say. nat64 is nil when NAT64Instances refused it, which skips the checks of the NAT
// rules against the NAT64s' prefixes and pools.
func (d Model) checkFirewall(nat64 []NAT64Instance) error {
	devices := d.Devices()
	var errs []error
	// chalkd listens in the default VRF alone, while the input hook sees the VRF's device for
	// each of its links. Only a Router document sets management interfaces.
	if r := d.Router(); len(d.Routers) > 0 {
		for j, i := range r.Management.Interfaces {
			errs = append(errs, d.checkManagement(d.Routers[0].Name, j, i, devices))
		}
	}
	zones, err := d.checkZones(devices)
	errs = append(errs, err)
	for _, v := range d.VRFs {
		if v.Spec.ConntrackZone != nil {
			if _, err := d.Membership.Links(v.Name); err != nil {
				errs = append(errs, err)
			}
		}
	}
	for _, r := range d.NAT {
		errs = append(errs, d.checkNATRule(r, devices, zones, nat64))
	}
	for _, p := range d.PolicyRoutes {
		errs = append(errs, d.checkPolicyRouteIn(p, devices, zones))
	}
	return errors.Join(errs...)
}

// checkManagement refuses the management interface or glob i, at index j of the Router router's
// management.interfaces, that matches a VRF's device or may be a link of a VRF, whose connections
// never reach chalkd, which answers in the default VRF alone.
func (d Model) checkManagement(router string, j int, i string, devices []string) error {
	at := fmt.Sprintf("spec.management.interfaces[%d]", j)
	if dev, ok := DeviceMatch(i, devices); ok {
		return refusals.Errorf("Router", router, at, "the management interface %s matches %s, the device of VRF %s, and chalkd answers in the default VRF alone; name the default VRF's links", i, dev, dev)
	}
	vrfs, err := d.Membership.InterfaceVRFs(i)
	if err != nil {
		return err
	}
	for _, v := range vrfs {
		if v != DefaultVRF {
			return refusals.Errorf("Router", router, at, "the management interface %s may be a link of VRF %s, and chalkd answers in the default VRF alone; keep the node API's interfaces out of every VRF", i, v)
		}
	}
	return nil
}

// zoneLinks are the links of the zone z as prerouting and postrouting match them: its interfaces
// and the links of its VRFs, as Membership.Links gives them, each once.
func (d Model) zoneLinks(z ZoneSpec) ([]string, error) {
	var links []string
	add := func(l string) {
		if !slices.Contains(links, l) {
			links = append(links, l)
		}
	}
	for _, i := range z.Interfaces {
		add(i)
	}
	for _, v := range z.VRFs {
		vl, err := d.Membership.Links(v)
		if err != nil {
			return nil, err
		}
		for _, l := range vl {
			add(l)
		}
	}
	return links, nil
}

// checkZones checks each zone and returns the links of those it accepts, by name, as zoneLinks
// gives them. The uniqueness check refuses a VRF that two zones hold.
func (d Model) checkZones(devices []string) (map[string][]string, error) {
	held := map[string]string{}
	for _, z := range d.Zones {
		for _, v := range z.Spec.VRFs {
			held[v] = z.Name
		}
	}
	out := map[string][]string{}
	var errs []error
	for _, z := range d.Zones {
		if err := d.checkZone(z, devices, held); err != nil {
			errs = append(errs, err)
			continue
		}
		links, err := d.zoneLinks(z.Spec)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out[z.Name] = links
	}
	return out, errors.Join(errs...)
}

// checkZone refuses the zone z at its first faulty interface. The forward chain handles a NAT64's
// link's traffic ahead of the zones. held are the zones that hold each VRF.
func (d Model) checkZone(z Named[ZoneSpec], devices []string, held map[string]string) error {
	for j, i := range z.Spec.Interfaces {
		at := fmt.Sprintf("spec.interfaces[%d]", j)
		if dev, ok := DeviceMatch(i, devices); ok {
			return refusals.Errorf("FirewallZone", z.Name, at, "%s matches %s, the device of VRF %s; name the VRF in vrfs", i, dev, dev)
		}
		vrfs, err := d.Membership.InterfaceVRFs(i)
		if err != nil {
			return err
		}
		for _, v := range vrfs {
			if other, ok := held[v]; ok && other != z.Name {
				return refusals.Errorf("FirewallZone", z.Name, at, "FirewallZone %s names %s, which may be a link of VRF %s, which FirewallZone %s holds; give each link one zone", z.Name, i, v, other)
			}
		}
		if link, n, ok := d.nat64Link(i); ok {
			return refusals.Errorf("FirewallZone", z.Name, at, "%s matches %s, the link of NAT64 %s, which no zone holds; name other links", i, link, n)
		}
	}
	return nil
}

// nat64Link is the link of the first NAT64 whose link the interface or glob i matches, and the
// NAT64's name.
func (d Model) nat64Link(i string) (string, string, bool) {
	for _, n := range d.NAT64 {
		if link := NAT64LinkPrefix + n.Name; MatchesGlob(i, link) {
			return link, n.Name, true
		}
	}
	return "", "", false
}

// checkNATRule refuses the NAT rule r at its first fault. Prerouting and postrouting translate by
// the links alone and leave a NAT64's link to the NAT64, so an interface must match some other
// link. A NAT64's pool belongs to the NAT64 whole, and the firewall accepts its connections before
// any NAT rule. zones are the links of the zones checkZones accepts; a rule of a zone it refuses
// goes unchecked.
func (d Model) checkNATRule(r Named[NATSpec], devices []string, zones map[string][]string, nat64 []NAT64Instance) error {
	n := r.Spec
	for _, place := range []struct {
		*NATPlace
		path string
	}{{n.In, "spec.in"}, {n.Out, "spec.out"}} {
		if place.NATPlace == nil || place.Interface == nil {
			continue
		}
		i := *place.Interface
		if dev, ok := d.onlyDevice(devices, i); ok {
			return refusals.Errorf("NATRule", r.Name, place.path+".interface", "%s matches no link, only %s, the device of VRF %s; name the VRF's links or a zone of the VRF", i, dev, dev)
		}
		if link, nat, ok := d.nat64Link(i); ok && !d.matchesOtherThanNAT64(i) {
			return refusals.Errorf("NATRule", r.Name, place.path+".interface", "%s matches no link but %s, the link of NAT64 %s, which no NAT rule translates; name other links", i, link, nat)
		}
	}
	// The place a rule translates at: out for masquerade and snat, in for dnat.
	place, at := n.Out, "spec.out"
	if n.Type == "dnat" {
		place, at = n.In, "spec.in"
	}
	if place != nil && place.Zone != nil {
		if links, ok := zones[*place.Zone]; !ok {
			return nil
		} else if len(links) == 0 {
			return refusals.Errorf("NATRule", r.Name, at+".zone", "the zone %s holds no link", *place.Zone)
		}
	}
	if n.Type == "dnat" {
		for i, s := range n.Match.Destination {
			dst := s.Masked()
			for _, x := range nat64 {
				for _, p := range append(slices.Clone(x.Pools), x.Prefix) {
					if p.Bits() <= dst.Bits() && p.Contains(dst.Addr()) {
						return refusals.Errorf("NATRule", r.Name, fmt.Sprintf("spec.match.destination[%d]", i), "match.destination %s lies in %s of NAT64 %s, whose connections the firewall accepts before any NATRule; match a range outside it", dst, p, x.Name)
					}
				}
			}
		}
	}
	if (n.Type == "snat" || n.Type == "dnat") && n.To != nil {
		a := n.To.Address
		for _, x := range nat64 {
			for _, p := range x.Pools {
				if p.Contains(a) {
					return refusals.Errorf("NATRule", r.Name, "spec.to.address", "to.address %s lies in %s, the pool of NAT64 %s, whose addresses belong to the NAT64 whole; translate to an address outside the pool", a, p, x.Name)
				}
			}
		}
	}
	return nil
}

// onlyDevice is the first VRF device the interface or glob i matches, when i matches no link the
// Network documents name and no glob of a VRF's links. An interface that is a device's name
// matches that device alone.
func (d Model) onlyDevice(devices []string, i string) (string, bool) {
	dev, ok := DeviceMatch(i, devices)
	if !ok || !strings.HasSuffix(i, "*") {
		return dev, ok
	}
	for _, l := range d.Links {
		if !slices.Contains(devices, l) && MatchesGlob(i, l) {
			return "", false
		}
	}
	for _, m := range d.Membership.Members {
		if GlobsOverlap(i, m.Glob) {
			return "", false
		}
	}
	return dev, true
}

// matchesOtherThanNAT64 reports whether the interface or glob i may match a link that is no
// NAT64's: a link the Network documents name, or one of a VRF's members.
func (d Model) matchesOtherThanNAT64(i string) bool {
	for _, l := range d.Links {
		if _, _, isNAT64 := d.nat64Link(l); !isNAT64 && MatchesGlob(i, l) {
			return true
		}
	}
	return slices.ContainsFunc(d.Membership.Members, func(m Member) bool { return GlobsOverlap(i, m.Glob) })
}

// checkPolicyRouteIn refuses the PolicyRoute p at the first fault of in. Jool translates a NAT64's
// link's traffic, and prerouting sees a VRF's device only after the pass that decides the mark, so
// in may match neither. zones are as for checkNATRule.
func (d Model) checkPolicyRouteIn(p Named[PolicyRouteSpec], devices []string, zones map[string][]string) error {
	in := p.Spec.In
	switch {
	case in == nil:
	case in.Zone != nil:
		links, ok := zones[*in.Zone]
		if !ok {
			return nil
		}
		if len(links) == 0 {
			return refusals.Errorf("PolicyRoute", p.Name, "spec.in.zone", "the zone %s holds no link", *in.Zone)
		}
		for _, l := range links {
			if link, n, ok := d.nat64Link(l); ok {
				return refusals.Errorf("PolicyRoute", p.Name, "spec.in.zone", "the zone %s holds %s, which matches %s, the link of NAT64 %s, whose traffic Jool translates; name the zone's other links in interfaces", *in.Zone, l, link, n)
			}
		}
	default:
		for j, i := range in.Interfaces {
			at := fmt.Sprintf("spec.in.interfaces[%d]", j)
			if dev, ok := DeviceMatch(i, devices); ok {
				return refusals.Errorf("PolicyRoute", p.Name, at, "%s matches %s, the device of VRF %s; name the VRF's links or a zone of the VRF", i, dev, dev)
			}
			if link, n, ok := d.nat64Link(i); ok {
				return refusals.Errorf("PolicyRoute", p.Name, at, "%s matches %s, the link of NAT64 %s, whose traffic Jool translates; name other links", i, link, n)
			}
		}
	}
	return nil
}
