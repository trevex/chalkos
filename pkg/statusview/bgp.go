package statusview

import (
	"cmp"
	"fmt"
	"io"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
)

// WriteBGP writes the peer table or why its state is unknown. The console passes a function
// that escapes display fields, while the CLI can pass nil.
func WriteBGP(w io.Writer, peers []*nodev1.BGPPeerStatus, problem string, clean func(string) string) error {
	if clean == nil {
		clean = func(s string) string { return s }
	}
	switch {
	case problem != "":
		fmt.Fprintf(w, "bgp: unknown: %s\n", clean(problem))
		return nil
	case len(peers) == 0:
		fmt.Fprintln(w, "bgp: no sessions")
		return nil
	}
	fmt.Fprintln(w, "bgp:")
	t := Table(w)
	fmt.Fprintln(t, "  VRF\tPEER\tNEIGHBOR\tSTATE\tUPTIME\tRECEIVED\tSENT\tBFD")
	for _, p := range peers {
		uptime := "-"
		if p.State == "Established" {
			uptime = (time.Duration(p.UptimeSeconds) * time.Second).String()
		}
		fmt.Fprintf(t, "  %s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\n", clean(cmp.Or(p.Vrf, "default")), clean(p.Name), clean(p.Neighbor), clean(p.State), uptime, p.PrefixesReceived, p.PrefixesSent, clean(cmp.Or(p.Bfd, "-")))
	}
	return t.Flush()
}
