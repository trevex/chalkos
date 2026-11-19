package derive

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/refusals"
)

// checkListeners refuses, each document at its first fault, an address of listen of a Resolver or
// a TimeServer that Network documents give only to links of other VRFs than the service's: its
// sockets are bound to its VRF, a TimeServer's chrony to the default VRF, and never see its
// queries. An address the documents do not give, such as one from DHCP, passes. The set check has
// refused two resolvers of one VRF.
func (d Model) checkListeners() error {
	var errs []error
	for _, r := range d.Resolvers {
		errs = append(errs, d.checkListen("Resolver", r.Name, "the resolver", VRFOf(r.Spec.VRF), r.Spec.Listen))
	}
	for _, s := range d.TimeServers {
		errs = append(errs, d.checkListen("TimeServer", s.Name, "the time server", "default", s.Spec.Listen))
	}
	return errors.Join(errs...)
}

// checkListen refuses the first address of listen, of the service of the document kind/name in
// the VRF vrf, that Network documents give only to links of other VRFs.
func (d Model) checkListen(kind, name, service, vrf string, listen []netip.Addr) error {
	for i, a := range listen {
		var others []string
		mine := false
		for _, n := range d.Addresses {
			if n.Prefix.Addr() != a {
				continue
			}
			if v := d.vrfOfNetwork(n.Network); v == vrf {
				mine = true
			} else if other := "Network " + n.Network + " in VRF " + v; !slices.Contains(others, other) {
				others = append(others, other)
			}
		}
		if !mine && len(others) > 0 {
			return refusals.Errorf(kind, name, fmt.Sprintf("spec.listen[%d]", i), "the address %s of listen belongs to links of %s alone, so %s of VRF %s never receives its queries; listen on an address of the VRF's links", a, strings.Join(others, " and "), service, vrf)
		}
	}
	return nil
}
