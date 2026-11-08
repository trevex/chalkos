package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/trevex/chalkos/pkg/router/derive"
	"github.com/trevex/chalkos/pkg/router/keaconf"
)

// KeaSocket is the control socket of the server of family, dhcp4 or dhcp6, of the VRF vrf, a name
// that matched ShortName or default, in the directory dir, /run/kea on a node: <family>.sock for
// the default VRF's server and <family>-<vrf>.sock for a VRF's, as the output kea renders them.
func KeaSocket(dir, family, vrf string) string {
	if vrf == derive.DefaultVRF {
		return filepath.Join(dir, family+".sock")
	}
	return filepath.Join(dir, family+"-"+vrf+".sock")
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

// ParseKeaSubnets reads the subnets of a DHCPv4 or DHCPv6 config of the output kea, in its order.
func ParseKeaSubnets(data []byte) ([]KeaSubnet, error) {
	var c keaconf.Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("read Kea's config: %w", err)
	}
	var subnets []keaconf.Subnet
	if c.Dhcp4 != nil {
		for _, s := range c.Dhcp4.Subnet4 {
			subnets = append(subnets, s.Subnet)
		}
	}
	if c.Dhcp6 != nil {
		for _, s := range c.Dhcp6.Subnet6 {
			subnets = append(subnets, s.Subnet)
		}
	}
	var out []KeaSubnet
	for _, s := range subnets {
		out = append(out, KeaSubnet{ID: s.ID, Name: s.UserContext.Name, Subnet: s.Subnet})
	}
	return out, nil
}

// ParseKeaInterfaces reads the links a DHCPv4 or DHCPv6 config of the output kea serves, which Kea
// refuses to start or reload without: those of its Dhcp4 section, or of its Dhcp6 section without
// one. A config whose section lists no interfaces, not even an empty list, is no config of the
// output.
func ParseKeaInterfaces(data []byte) ([]string, error) {
	var c keaconf.Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("read Kea's config: %w", err)
	}
	var s *keaconf.Server
	if c.Dhcp4 != nil {
		s = &c.Dhcp4.Server
	} else if c.Dhcp6 != nil {
		s = &c.Dhcp6.Server
	}
	if s == nil || s.InterfacesConfig.Interfaces == nil {
		return nil, errors.New("read Kea's config: it lists no interfaces")
	}
	return s.InterfacesConfig.Interfaces, nil
}

// KeaLeases are a subnet's counts of Kea's statistics: the addresses it leased, reserved ones
// included, and those of its pools, and the prefixes it delegated and those it may delegate.
// Kea counts an IPv6 pool's addresses in 128 bits; a count past 64 bits is the largest of 64.
type KeaLeases struct {
	Assigned, Total      uint64
	Delegated, Delegable uint64
}

// keaSubnetStatistic is a statistic of a subnet as statistic-get-all names it: addresses of a
// DHCPv4 server, nas (addresses) and pds (prefixes) of a DHCPv6 one.
var keaSubnetStatistic = regexp.MustCompile(`^subnet\[([0-9]+)\]\.(assigned|total)-(addresses|nas|pds)$`)

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
		v, err := strconv.ParseUint(string(samples[0][0]), 10, 64)
		if errors.Is(err, strconv.ErrRange) {
			v, err = math.MaxUint64, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read Kea's statistic %s: %q is no count", name, samples[0][0])
		}
		l := out[uint32(id)]
		switch m[2] + "-" + m[3] {
		case "assigned-addresses", "assigned-nas":
			l.Assigned = v
		case "total-addresses", "total-nas":
			l.Total = v
		case "assigned-pds":
			l.Delegated = v
		case "total-pds":
			l.Delegable = v
		}
		out[uint32(id)] = l
	}
	return out, nil
}
