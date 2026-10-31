package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
)

// KeaFile is the config of the default VRF's DHCPv4 server that the output kea renders; a VRF's is
// kea-dhcp4-<vrf>.json.
const KeaFile = "kea-dhcp4.json"

// KeaSocket is the control socket of the DHCPv4 server of the VRF vrf, a name that matched
// ShortName or default, in the directory dir, /run/kea on a node: dhcp4.sock for the default
// VRF's server and dhcp4-<vrf>.sock for a VRF's, as the output kea renders them.
func KeaSocket(dir, vrf string) string {
	if vrf == "default" {
		return filepath.Join(dir, "dhcp4.sock")
	}
	return filepath.Join(dir, "dhcp4-"+vrf+".sock")
}

// keaResult is Kea's answer to a command: result 0 for success, 3 for an empty answer, such as no
// leases, and others for failures that text explains.
type keaResult struct {
	Result    int             `json:"result"`
	Text      string          `json:"text"`
	Arguments json.RawMessage `json:"arguments"`
}

// KeaAsk sends a command without arguments to the Kea server whose control socket is socket, and
// returns the arguments of its answer, until ctx ends.
func KeaAsk(ctx context.Context, socket, command string) (json.RawMessage, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	// The command is a constant of chalkd's; no value of a request reaches it.
	if _, err := fmt.Fprintf(conn, `{"command": %q}`, command); err != nil {
		return nil, err
	}
	var r keaResult
	if err := json.NewDecoder(conn).Decode(&r); err != nil {
		return nil, fmt.Errorf("read Kea's answer to %s: %w", command, err)
	}
	if r.Result != 0 && r.Result != 3 {
		return nil, fmt.Errorf("Kea answered %s with result %d: %s", command, r.Result, r.Text)
	}
	return r.Arguments, nil
}

// KeaSubnet is a subnet of a config of the output kea: Kea's number for it, its DHCPSubnet
// document's name and its range.
type KeaSubnet struct {
	ID     uint32
	Name   string
	Subnet string
}

// ParseKeaSubnets reads the subnets of a config of the output kea, in its order.
func ParseKeaSubnets(data []byte) ([]KeaSubnet, error) {
	var c struct {
		Dhcp4 struct {
			Subnet4 []struct {
				ID          uint32 `json:"id"`
				Subnet      string `json:"subnet"`
				UserContext struct {
					Name string `json:"name"`
				} `json:"user-context"`
			} `json:"subnet4"`
		} `json:"Dhcp4"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("read Kea's config: %w", err)
	}
	var out []KeaSubnet
	for _, s := range c.Dhcp4.Subnet4 {
		out = append(out, KeaSubnet{ID: s.ID, Name: s.UserContext.Name, Subnet: s.Subnet})
	}
	return out, nil
}

// KeaLeases are a subnet's counts of Kea's statistics: the addresses it leased, reserved ones
// included, and those of its pools.
type KeaLeases struct {
	Assigned, Total uint64
}

// keaSubnetStatistic is a statistic of a subnet as statistic-get-all names it.
var keaSubnetStatistic = regexp.MustCompile(`^subnet\[([0-9]+)\]\.(assigned|total)-addresses$`)

// ParseKeaStatistics reads the arguments of statistic-get-all: each subnet's counts, by Kea's
// number for it. Kea lists each statistic's samples, the newest first, each a value and its time.
func ParseKeaStatistics(args json.RawMessage) (map[uint32]KeaLeases, error) {
	var stats map[string][][]json.RawMessage
	if err := json.Unmarshal(args, &stats); err != nil {
		return nil, fmt.Errorf("read Kea's statistics: %w", err)
	}
	out := map[uint32]KeaLeases{}
	for name, samples := range stats {
		m := keaSubnetStatistic.FindStringSubmatch(name)
		if m == nil || len(samples) == 0 || len(samples[0]) == 0 {
			continue
		}
		id, err := strconv.ParseUint(m[1], 10, 32)
		if err != nil {
			continue
		}
		var v uint64
		if err := json.Unmarshal(samples[0][0], &v); err != nil {
			return nil, fmt.Errorf("read Kea's statistic %s: %w", name, err)
		}
		l := out[uint32(id)]
		if m[2] == "assigned" {
			l.Assigned = v
		} else {
			l.Total = v
		}
		out[uint32(id)] = l
	}
	return out, nil
}
