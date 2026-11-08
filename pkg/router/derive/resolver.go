package derive

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/refusals"
)

// checkResolvers refuses, each Resolver at its first fault, an address of listen that Network
// documents give only to links of other VRFs than the resolver's: Unbound's sockets are bound to
// its VRF and never see its queries. An address the documents do not give, such as one from DHCP,
// passes. The set check has refused two resolvers of one VRF.
func (d Model) checkResolvers() error {
	var errs []error
	for _, r := range d.Resolvers {
		vrf := VRFOf(r.Spec.VRF)
		for i, a := range r.Spec.Listen {
			var others []string
			mine := false
			for _, n := range d.Addresses {
				if n.Prefix.Addr() != a {
					continue
				}
				if v := d.VRFOfNetwork(n.Network); v == vrf {
					mine = true
				} else if other := "Network " + n.Network + " in VRF " + v; !slices.Contains(others, other) {
					others = append(others, other)
				}
			}
			if !mine && len(others) > 0 {
				errs = append(errs, refusals.Errorf("Resolver", r.Name, fmt.Sprintf("spec.listen[%d]", i), "the address %s of listen belongs to links of %s alone, so the resolver of VRF %s never receives its queries; listen on an address of the VRF's links", a, strings.Join(others, " and "), vrf))
				break
			}
		}
	}
	return errors.Join(errs...)
}
