// Package vxlan fills the nftables table chalkos-vxlan, which lets the firewall accept flannel's
// VXLAN only when it is sent to the node's addresses.
//
// The NixOS firewall drops what its own input chain does not accept, whatever other tables do, so
// the table only marks VXLAN packets sent to the node's addresses, and the firewall accepts
// packets carrying Mark (see modules/node/kubernetes/firewall.nix). The table is not the
// firewall's: reloading the firewall leaves it alone. With source ranges the table also drops the
// VXLAN pods send through the node to them.
package vxlan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"

	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
)

const (
	// tableName is the name of the table, of the inet family.
	tableName = "chalkos-vxlan"
	// Mark is the packet mark bit the firewall accepts VXLAN with. kube-proxy (0x4000, 0x8000)
	// and flannel leave it alone.
	Mark = "0x01000000"
	// vxlanPort is flannel's VXLAN port.
	vxlanPort = 8472
	// lockFile serialises the runs on a node.
	lockFile = "/run/chalkos-vxlan-rule.lock"
)

// Destination is an address of the node that takes VXLAN.
type Destination struct {
	IP netip.Addr
	// AnyInterface takes VXLAN arriving on any interface but those of the pod network and
	// kube-proxy; otherwise VXLAN must arrive on the interface holding IP.
	AnyInterface bool
}

// DestinationOf is the destination of a node address. An address on a network interface takes
// VXLAN on that interface alone. One on a loopback or dummy interface is an address routers reach
// the node at through its other interfaces, as with a routing daemon that announces it, so VXLAN
// to it arrives on any interface.
func DestinationOf(a nodeip.Address) Destination {
	return Destination{IP: a.IP, AnyInterface: a.Link.Loopback || a.Link.Kind == "dummy"}
}

// Accept is what the table accepts: VXLAN to the destinations, at most one per family, from the
// sources, or from any source without them.
type Accept struct {
	Destinations []Destination
	Sources      []netip.Prefix
}

// Marshal writes a line "destination <address> <interface|any>" for each destination and a line
// "source <range>" for each source range.
func (a Accept) Marshal() []byte {
	var b strings.Builder
	for _, d := range a.Destinations {
		mode := "interface"
		if d.AnyInterface {
			mode = "any"
		}
		fmt.Fprintf(&b, "destination %s %s\n", d.IP, mode)
	}
	for _, s := range a.Sources {
		fmt.Fprintf(&b, "source %s\n", s)
	}
	return []byte(b.String())
}

// parseAccept reads what Marshal writes. Every line must be one Marshal writes, with at least one
// destination and at most one per family; addresses with a zone are refused.
func parseAccept(data []byte) (Accept, error) {
	var a Accept
	text := string(data)
	if text == "" {
		return Accept{}, errors.New("no destination")
	}
	families := map[string]bool{}
	for line := range strings.SplitSeq(strings.TrimSuffix(text, "\n"), "\n") {
		fields := strings.Split(line, " ")
		switch {
		case len(fields) == 3 && fields[0] == "destination":
			ip, err := netip.ParseAddr(fields[1])
			if err != nil || ip.Zone() != "" {
				return Accept{}, fmt.Errorf("%q is not an address", fields[1])
			}
			if families[family(ip)] {
				return Accept{}, fmt.Errorf("a second %s destination %s", family(ip), ip)
			}
			families[family(ip)] = true
			if fields[2] != "interface" && fields[2] != "any" {
				return Accept{}, fmt.Errorf("%q is neither interface nor any", fields[2])
			}
			a.Destinations = append(a.Destinations, Destination{IP: ip, AnyInterface: fields[2] == "any"})
		case len(fields) == 2 && fields[0] == "source":
			p, err := netip.ParsePrefix(fields[1])
			if err != nil {
				return Accept{}, fmt.Errorf("%q is not a range in CIDR notation", fields[1])
			}
			a.Sources = append(a.Sources, p.Masked())
		default:
			return Accept{}, fmt.Errorf("the line %q", line)
		}
	}
	if len(a.Destinations) == 0 {
		return Accept{}, errors.New("no destination")
	}
	return a, nil
}

// family is the nft family of an address: ip or ip6.
func family(ip netip.Addr) string {
	if ip.Is4() {
		return "ip"
	}
	return "ip6"
}

// dropAll is the forward chain that drops all VXLAN the node forwards, the chain until the
// cluster's ranges are read and when they cannot be.
var dropAll = []string{fmt.Sprintf("udp dport %d drop", vxlanPort)}

// forwardRules returns the forward chain's rules from the cluster's ranges. The node masquerades what
// pods send to other nodes behind its own address, which is in the source ranges, so pods could
// send VXLAN the other nodes take as this node's. The chain drops it as the node forwards it, per
// family of the cluster with source ranges; flannel's own VXLAN is the node's, not forwarded.
// Source ranges of a family the cluster lacks are skipped. A family without a pod range is an
// error, which the caller answers with dropAll.
func forwardRules(c kubernetes.Cluster) ([]string, error) {
	var rules []string
	for _, f := range c.Families() {
		fam := "ip"
		if f == nodeip.IPv6 {
			fam = "ip6"
		}
		pods := c.PodCIDRs.Of(f)
		if !pods.IsValid() || family(pods.Addr()) != fam {
			return nil, fmt.Errorf("no %s pod range", f)
		}
		var sources []netip.Prefix
		for _, s := range c.VXLANSources() {
			if s.IsValid() && family(s.Addr()) == fam {
				sources = append(sources, s.Masked())
			}
		}
		if len(sources) > 0 {
			rules = append(rules, fmt.Sprintf("%s saddr { %s } %s daddr { %s } udp dport %d drop",
				fam, pods.Masked(), fam, join(sources), vxlanPort))
		}
	}
	return rules, nil
}

// readForward reads the cluster file without checking the rest of the cluster, so the forward
// chain stands whatever else the file or the preparation gets wrong.
func readForward(clusterFile string) ([]string, error) {
	data, err := os.ReadFile(clusterFile)
	if err != nil {
		return nil, err
	}
	var c kubernetes.Cluster
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", clusterFile, err)
	}
	return forwardRules(c)
}

// ruleset is the table, replaced in one transaction, marking VXLAN as accept says and with the
// forward rules given.
//   - Its input chain runs before the firewall's, which drops what it does not accept, and first
//     clears the mark, so only these rules set it whatever ran earlier.
//   - Its clear chain runs after the firewall's, so the mark never outlives it: not on packets the
//     firewall accepted before looking at it, such as those on lo or of established connections,
//     nor on the packets VXLAN carries.
func ruleset(accept Accept, forward []string) string {
	clear := fmt.Sprintf("meta mark & %[1]s == %[1]s meta mark set meta mark ^ %[1]s", Mark)
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %[1]s\ndelete table inet %[1]s\ntable inet %[1]s {\n", tableName)
	b.WriteString("  chain input {\n    type filter hook input priority filter - 1; policy accept;\n")
	b.WriteString("    " + clear + "\n")
	for _, rule := range inputRules(accept) {
		b.WriteString("    " + rule + "\n")
	}
	b.WriteString("  }\n")
	b.WriteString("  chain clear {\n    type filter hook input priority filter + 1; policy accept;\n")
	b.WriteString("    " + clear + "\n  }\n")
	if len(forward) > 0 {
		b.WriteString("  chain forward {\n    type filter hook forward priority filter; policy accept;\n")
		for _, rule := range forward {
			b.WriteString("    " + rule + "\n")
		}
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// inputRules mark VXLAN to each destination. With source ranges a family without one takes no
// VXLAN. The ranges limit who may send VXLAN; within them a source may be forged. The kernel
// drops an IPv4 packet with a source of the node's own arriving from the network (accept_local
// is off), but IPv6 has no such check.
func inputRules(accept Accept) []string {
	var interfaces []string
	for _, prefix := range nodeip.KubernetesInterfaces() {
		interfaces = append(interfaces, fmt.Sprintf("%q", prefix+"*"))
	}
	slices.Sort(interfaces)
	var rules []string
	for _, d := range accept.Destinations {
		fam := family(d.IP)
		rule := fmt.Sprintf("%s daddr %s udp dport %d", fam, d.IP, vxlanPort)
		if len(accept.Sources) > 0 {
			var sources []netip.Prefix
			for _, s := range accept.Sources {
				if family(s.Addr()) == fam {
					sources = append(sources, s)
				}
			}
			if len(sources) == 0 {
				continue
			}
			rule = fmt.Sprintf("%s saddr { %s } %s", fam, join(sources), rule)
		}
		if d.AnyInterface {
			// Not on the interfaces of the pod network and kube-proxy, where pods could send it.
			rule += " iifname != { " + strings.Join(interfaces, ", ") + " }"
		} else {
			rule += " fib daddr . iif type local"
		}
		rules = append(rules, rule+" meta mark set meta mark | "+Mark)
	}
	return rules
}

func join(prefixes []netip.Prefix) string {
	list := make([]string, len(prefixes))
	for i, p := range prefixes {
		list[i] = p.String()
	}
	return strings.Join(list, ", ")
}

// Rule fills the table on a node.
type Rule struct {
	// LockFile serialises the runs: the preparation runs the rule before and after it picks the
	// node's addresses, and the last run must see the files as they are then.
	LockFile string
	// ClusterFile is the cluster the forward chain's ranges come from.
	ClusterFile string
	// NFT runs nft with the arguments, the input on its standard input.
	NFT func(input string, args ...string) error
}

// Default is the Rule of a node, running the nft on the PATH.
func Default() Rule {
	return Rule{LockFile: lockFile, ClusterFile: kubernetes.ClusterFile, NFT: runNFT}
}

func runNFT(input string, args ...string) error {
	cmd := exec.Command("nft", args...)
	cmd.Stdin = strings.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = os.Stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("nft: %w: %s", err, msg)
		}
		return fmt.Errorf("nft: %w", err)
	}
	return nil
}

// Apply fills the table from file, which holds what Accept.Marshal writes, and empties its input
// chain without file, given as "" or missing. The forward chain comes from the cluster's ranges
// on every run, whatever file holds, so it stands while the cluster has them; when they cannot be
// read it drops all VXLAN the node forwards. A file that cannot be read or holds anything else
// leaves the input chain empty and fails. A ruleset nft refuses changes nothing, so then the
// input chain is emptied and forwarded VXLAN dropped as without the ranges. Should nft refuse
// even that, the table is deleted: no rule of an earlier run stays. The table goes to the
// standard output.
func (r Rule) Apply(file string) error {
	unlock, err := lock(r.LockFile)
	if err != nil {
		return errors.Join(fmt.Errorf("lock %s: %w", r.LockFile, err), r.empty(dropAll))
	}
	defer unlock()

	forward, err := readForward(r.ClusterFile)
	if err != nil {
		log.Printf("the cluster's ranges are unknown: %v; pods' VXLAN is dropped wherever it goes", err)
		forward = dropAll
	}

	var accept Accept
	var refused error
	if file != "" {
		data, err := os.ReadFile(file)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return errors.Join(err, r.empty(forward))
		default:
			if accept, err = parseAccept(data); err != nil {
				refused = fmt.Errorf("%s does not hold the node's addresses: %w; VXLAN stays refused", file, err)
			}
		}
	}

	if err := r.NFT(ruleset(accept, forward), "-f", "-"); err != nil {
		refused = fmt.Errorf("VXLAN stays refused and forwarded VXLAN is dropped: %w", err)
		if err := r.empty(dropAll); err != nil {
			return errors.Join(refused, err)
		}
	}
	if err := r.NFT("", "list", "table", "inet", tableName); err != nil {
		return errors.Join(refused, err)
	}
	return refused
}

// empty empties the input chain, with the forward rules given, or deletes the table should nft
// refuse that.
func (r Rule) empty(forward []string) error {
	err := r.NFT(ruleset(Accept{}, forward), "-f", "-")
	if err == nil {
		return nil
	}
	if derr := r.NFT("", "delete", "table", "inet", tableName); derr != nil {
		return errors.Join(err, derr)
	}
	return nil
}

// lock takes the lock file, waiting for other runs.
func lock(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}
