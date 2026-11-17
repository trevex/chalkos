// Package status reads BGP sessions and routes from the local FRR daemons.
package status

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/tool"
)

// NeighborsCommand lists the sessions of every VRF's instance.
const NeighborsCommand = "show bgp vrf all neighbors json"

// ReadPeers reads FRR's local sessions without involving the cluster.
func ReadPeers(ctx context.Context, run tool.Runner) ([]*nodev1.BGPPeerStatus, error) {
	out, err := Query(ctx, run, NeighborsCommand)
	if err != nil {
		return nil, err
	}
	return ParsePeers(out)
}

// Query runs a fixed vtysh show command with a bounded wait.
func Query(ctx context.Context, run tool.Runner, command string) ([]byte, error) {
	const timeout = 5 * time.Second
	budget, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := run.Exec(budget, tool.Cmd{Name: "vtysh", Args: []string{"-c", command}, Quiet: true})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(budget.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("vtysh did not answer within %v", timeout)
		}
		return nil, err
	}
	return out, nil
}

func vrfOrder(name string) string {
	if name == "default" {
		return ""
	}
	return name
}

// neighbor is a session as show bgp vrf all neighbors json prints it.
type neighbor struct {
	PeerGroup         string `json:"peerGroup"`
	State             string `json:"bgpState"`
	UpMsec            uint64 `json:"bgpTimerUpMsec"`
	AddressFamilyInfo map[string]struct {
		Accepted uint32 `json:"acceptedPrefixCounter"`
		Sent     uint32 `json:"sentPrefixCounter"`
	} `json:"addressFamilyInfo"`
	BFD *struct {
		Status string `json:"status"`
	} `json:"peerBfdInfo"`
}

// ParsePeers reads the sessions of show bgp vrf all neighbors json, an object per VRF of its
// vrfId, its vrfName and its sessions keyed by interface or address. It orders sessions by VRF,
// default first, then by BGPPeer document and neighbour. A session of no
// BGPPeer's peer group is left out; FRR prints {} without a BGP instance.
func ParsePeers(data []byte) ([]*nodev1.BGPPeerStatus, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("bgpd did not answer")
	}
	var vrfs map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &vrfs); err != nil {
		return nil, fmt.Errorf("read FRR's BGP neighbours: %w", err)
	}
	var peers []*nodev1.BGPPeerStatus
	for vrf, entries := range vrfs {
		for key, raw := range entries {
			// vrfId and vrfName are no sessions.
			if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
				continue
			}
			var n neighbor
			if err := json.Unmarshal(raw, &n); err != nil {
				return nil, fmt.Errorf("read FRR's BGP neighbour %s of the VRF %s: %w", key, vrf, err)
			}
			name, ok := strings.CutPrefix(n.PeerGroup, "peer-")
			if !ok {
				continue
			}
			p := &nodev1.BGPPeerStatus{Vrf: vrf, Name: name, Neighbor: key, State: n.State}
			if n.State == "Established" {
				p.UptimeSeconds = n.UpMsec / 1000
			}
			for _, af := range n.AddressFamilyInfo {
				p.PrefixesReceived += af.Accepted
				p.PrefixesSent += af.Sent
			}
			if n.BFD != nil {
				p.Bfd = bfdState(n.BFD.Status)
			}
			peers = append(peers, p)
		}
	}
	slices.SortFunc(peers, func(a, b *nodev1.BGPPeerStatus) int {
		return cmp.Or(strings.Compare(vrfOrder(a.Vrf), vrfOrder(b.Vrf)), strings.Compare(a.Name, b.Name), strings.Compare(a.Neighbor, b.Neighbor))
	})
	return peers, nil
}

// bfdState is FRR's BFD status, Up, Down, Admin Down or Unknown, as the status names it: up, down,
// admin-down or unknown.
func bfdState(s string) string {
	switch s := strings.ToLower(s); s {
	case "up", "down":
		return s
	case "admin down":
		return "admin-down"
	}
	return "unknown"
}
