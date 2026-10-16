// Package identity holds a node's identity: the form its config translates to, which the node
// applies at boot (/state/identity.json, /run/chalkos/node.json) and its Kubernetes side reads,
// and the kinds of Kubernetes nodes. It imports nothing of chalkos but the storage section, so
// every package on the node may use it.
package identity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	"github.com/trevex/chalkos/pkg/storage"
)

// Kinds of Kubernetes nodes.
const (
	KindControlPlane = "controlplane"
	KindWorker       = "worker"
)

// Identity is the node's identity without secrets, as the node applies it.
type Identity struct {
	Hostname string `json:"hostname"`
	// Cluster is the cluster's name, which the node's Cluster document carries, and Platform the
	// platform the node's image is built for, which its config is translated for.
	Cluster  string         `json:"cluster"`
	Platform string         `json:"platform"`
	Network  map[string]any `json:"network"`
	// NetworkUnits are the systemd-networkd unit files rendered from Network, by file name.
	NetworkUnits map[string]string `json:"networkUnits"`
	Labels       map[string]string `json:"labels"`
	Taints       []Taint           `json:"taints"`
	// Storage is how the node partitions, encrypts and mounts its disks.
	Storage    storage.Section `json:"storage"`
	Kubernetes *Kubernetes     `json:"kubernetes"`
	// Time is how the node keeps its clock.
	Time Time `json:"time"`
}

// Time names the servers the node keeps its clock with.
type Time struct {
	Servers []TimeServer `json:"servers"`
}

// TimeServer is a time server; Port zero means NTP's.
type TimeServer struct {
	Host string `json:"host"`
	NTS  bool   `json:"nts"`
	Port int    `json:"port,omitempty"`
}

// UnmarshalJSON reads a time server, authenticated with NTS unless "nts" is false, as the Nix
// option defaults to: a server is never left unauthenticated because a field was left out.
func (s *TimeServer) UnmarshalJSON(data []byte) error {
	type plain TimeServer
	server := plain{NTS: true}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&server); err != nil {
		return err
	}
	*s = TimeServer(server)
	return nil
}

// timeHost is what a time server's host may be: a host name or an address.
var timeHost = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]*$`)

// Check checks that each server has a host name or address and a port, if any, that is one.
func (t Time) Check() error {
	for _, s := range t.Servers {
		if !timeHost.MatchString(s.Host) {
			return fmt.Errorf("time server %q is not a host name or address", s.Host)
		}
		if s.Port < 0 || s.Port > 65535 {
			return fmt.Errorf("time server %s: port %d is out of range", s.Host, s.Port)
		}
	}
	return nil
}

// ChronySources are the servers as chrony's sources file lists them.
func (t Time) ChronySources() (string, error) {
	if err := t.Check(); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, s := range t.Servers {
		b.WriteString("server " + s.Host + " iburst")
		if s.NTS {
			b.WriteString(" nts")
		}
		if s.Port != 0 {
			fmt.Fprintf(&b, " port %d", s.Port)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// Kubernetes is what the node is in Kubernetes; nil on nodes of an image without Kubernetes.
type Kubernetes struct {
	// NodeName is the node's name in the cluster, which its Node object and kubelet
	// certificates carry.
	NodeName string `json:"nodeName"`
	// NodeIPs are the node's fixed addresses, at most one per address family; the node picks
	// the addresses of the other families at boot.
	NodeIPs []string `json:"nodeIPs"`
	// ValidSubnets are the subnets the node picks its addresses from instead of its cluster's;
	// nil uses the cluster's.
	ValidSubnets []string `json:"validSubnets"`
}

// CheckNodeIPs checks that the fixed addresses are addresses.
func (k Kubernetes) CheckNodeIPs() error {
	for _, ip := range k.NodeIPs {
		if _, err := netip.ParseAddr(ip); err != nil {
			return fmt.Errorf("nodeIPs: %q is not an address", ip)
		}
	}
	return nil
}

// StaticAddresses returns the addresses of the node's networkd networks, in network name order,
// without prefix lengths.
func (id Identity) StaticAddresses() []string {
	networks, _ := id.Network["networks"].(map[string]any)
	names := make([]string, 0, len(networks))
	for name := range networks {
		names = append(names, name)
	}
	sort.Strings(names)
	var addrs []string
	for _, name := range names {
		network, _ := networks[name].(map[string]any)
		list, _ := network["address"].([]any)
		for _, a := range list {
			if s, ok := a.(string); ok {
				addr, _, _ := strings.Cut(s, "/")
				addrs = append(addrs, addr)
			}
		}
	}
	return addrs
}

// Taint is a Kubernetes taint applied to the node.
type Taint struct {
	Key string `json:"key"`
	// Value is empty for a taint without a value.
	Value  string `json:"value"`
	Effect string `json:"effect"`
}
