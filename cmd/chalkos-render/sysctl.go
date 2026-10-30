package main

import (
	"fmt"
	"strings"
)

// sysctlFile is the file renderRouterSysctl writes. systemd-sysctl reads /run/sysctl.d with
// /etc/sysctl.d by file name, so it sorts after the image's 60-nixos.conf and its settings win.
const sysctlFile = "90-chalkos-router.conf"

// renderRouterSysctl writes the kernel settings of the node's Router to out/90-chalkos-router.conf:
// forwarding of each family, the multipath hash of both, the connection table's size and that the
// default VRF's sockets take no connections of a VRF's links. The firewall's reverse-path filter
// covers both families, so the kernel's own IPv4 filter is off on every interface, also on those
// that appear after systemd-sysctl ran.
func renderRouterSysctl(in map[string]any, out string) error {
	d, err := readRouterDocuments(in)
	if err != nil {
		return err
	}
	r := d.router()
	hash, ok := map[string]int{"l3": 0, "l4": 1}[r.ECMPHash]
	if !ok {
		return fmt.Errorf("Router: ecmpHash %q is neither l3 nor l4", r.ECMPHash)
	}
	if r.ConntrackMax != nil && *r.ConntrackMax < 1 {
		return fmt.Errorf("Router: conntrackMax %d is below 1", *r.ConntrackMax)
	}
	var b strings.Builder
	b.WriteString("# The kernel settings of the node's Router, rendered by chalkos-render router-sysctl.\n")
	fmt.Fprintf(&b, "net.ipv4.ip_forward = %d\n", boolInt(r.Forwarding.IPv4))
	fmt.Fprintf(&b, "net.ipv6.conf.all.forwarding = %d\n", boolInt(r.Forwarding.IPv6))
	fmt.Fprintf(&b, "net.ipv4.fib_multipath_hash_policy = %d\n", hash)
	fmt.Fprintf(&b, "net.ipv6.fib_multipath_hash_policy = %d\n", hash)
	for _, conf := range []string{"all", "default", "*"} {
		fmt.Fprintf(&b, "net.ipv4.conf.%s.rp_filter = 0\n", conf)
	}
	// A socket of the default VRF, such as chalkd's, never answers on a VRF's link; raw sockets
	// would by the kernel's default. The settings cover IPv6 as well.
	for _, proto := range []string{"tcp", "udp", "raw"} {
		fmt.Fprintf(&b, "net.ipv4.%s_l3mdev_accept = 0\n", proto)
	}
	if r.ConntrackMax != nil {
		fmt.Fprintf(&b, "net.netfilter.nf_conntrack_max = %d\n", *r.ConntrackMax)
	}
	return writeFile(out, sysctlFile, []byte(b.String()))
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
