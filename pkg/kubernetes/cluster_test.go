package kubernetes

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
)

const clusterJSON = `{
  "kind": "controlplane",
  "endpoint": "https://10.0.0.10:6443",
  "version": "1.37.1",
  "podCIDRs": {"ipv4": "10.244.0.0/16", "ipv6": "fd00:10:244::/56"},
  "serviceCIDRs": {"ipv4": "10.96.0.0/12", "ipv6": "fd00:10:96::/112"},
  "dnsIPs": {"ipv4": "10.96.0.10", "ipv6": "fd00:10:96::a"},
  "nodeCIDRMaskSizes": {"ipv4": 24, "ipv6": 64},
  "domain": "cluster.local",
  "allowSchedulingOnControlPlanes": false,
  "nodeIP": {"validSubnets": ["10.0.0.0/8", "!10.0.0.10/32"], "timeout": 300},
  "extraArgs": {"kube-apiserver": {"v": "2"}},
  "images": {"etcd": "registry.k8s.io/etcd:3.7.0-0", "kubeAPIServer": "registry.k8s.io/kube-apiserver:v1.37.1",
    "kubeControllerManager": "registry.k8s.io/kube-controller-manager:v1.37.1", "kubeScheduler": "registry.k8s.io/kube-scheduler:v1.37.1"}
}`

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadCluster(t *testing.T) {
	c, err := ReadCluster(writeFile(t, clusterJSON))
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != KindControlPlane || c.ExtraArgs["kube-apiserver"]["v"] != "2" || c.Images.Etcd != "registry.k8s.io/etcd:3.7.0-0" || c.NodeIP.Timeout != 300 {
		t.Errorf("cluster = %+v", c)
	}
	if host, err := c.EndpointHost(); err != nil || host != "10.0.0.10" {
		t.Errorf("EndpointHost() = %q, %v", host, err)
	}
	if ip, err := c.APIServerServiceIP(); err != nil || !ip.Equal(net.ParseIP("10.96.0.1")) {
		t.Errorf("APIServerServiceIP() = %v, %v", ip, err)
	}
}

func TestClusterRanges(t *testing.T) {
	c, err := ReadCluster(writeFile(t, clusterJSON))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		families       []string
		primary        nodeip.Family
		pods, services string
		apiServerSvcIP string
	}{
		{[]string{"ipv4"}, nodeip.IPv4, "10.244.0.0/16", "10.96.0.0/12", "10.96.0.1"},
		{[]string{"ipv4", "ipv6"}, nodeip.IPv4, "10.244.0.0/16 fd00:10:244::/56", "10.96.0.0/12 fd00:10:96::/112", "10.96.0.1"},
		{[]string{"ipv6", "ipv4"}, nodeip.IPv6, "fd00:10:244::/56 10.244.0.0/16", "fd00:10:96::/112 10.96.0.0/12", "fd00:10:96::1"},
		{[]string{"ipv6"}, nodeip.IPv6, "fd00:10:244::/56", "fd00:10:96::/112", "fd00:10:96::1"},
	} {
		c := c
		c.IPFamilies = tc.families
		if err := c.Validate(); err != nil {
			t.Errorf("%v: %v", tc.families, err)
		}
		if got := c.Primary(); got != tc.primary {
			t.Errorf("%v: Primary() = %s", tc.families, got)
		}
		pods, err := c.PodCIDRList()
		if err != nil || fmt.Sprint(pods) != "["+tc.pods+"]" {
			t.Errorf("%v: PodCIDRList() = %v, %v", tc.families, pods, err)
		}
		services, err := c.ServiceCIDRList()
		if err != nil || fmt.Sprint(services) != "["+tc.services+"]" {
			t.Errorf("%v: ServiceCIDRList() = %v, %v", tc.families, services, err)
		}
		if ip, err := c.APIServerServiceIP(); err != nil || !ip.Equal(net.ParseIP(tc.apiServerSvcIP)) {
			t.Errorf("%v: APIServerServiceIP() = %v, %v", tc.families, ip, err)
		}
	}

	// The checks of kubeadm, per family, naming the option and the value.
	for _, tc := range []struct {
		family string
		edit   func(c *Cluster)
		want   string
	}{
		{"ipv4", func(c *Cluster) { c.PodCIDRs.IPv4 = "" }, `podCIDRs.ipv4 "" is not an address range in CIDR notation`},
		{"ipv4", func(c *Cluster) { c.PodCIDRs.IPv4 = "10.244.0.0" }, `podCIDRs.ipv4 "10.244.0.0" is not an address range`},
		{"ipv4", func(c *Cluster) { c.PodCIDRs.IPv4 = "fd00:10:244::/56" }, "podCIDRs.ipv4 fd00:10:244::/56 is not an ipv4 range"},
		{"ipv6", func(c *Cluster) { c.ServiceCIDRs.IPv6 = "10.96.0.0/12" }, "serviceCIDRs.ipv6 10.96.0.0/12 is not an ipv6 range"},
		{"ipv6", func(c *Cluster) { c.PodCIDRs.IPv6 = "::ffff:10.244.0.0/112" }, "podCIDRs.ipv6 ::ffff:10.244.0.0/112 is an IPv4-mapped IPv6 range"},
		{"ipv4", func(c *Cluster) { c.ServiceCIDRs.IPv4 = "10.96.0.1/12" }, "serviceCIDRs.ipv4 10.96.0.1/12 has host bits set; write 10.96.0.0/12"},
		{"ipv4", func(c *Cluster) { c.ServiceCIDRs.IPv4 = "10.244.128.0/20" }, "podCIDRs.ipv4 10.244.0.0/16 and serviceCIDRs.ipv4 10.244.128.0/20 overlap"},
		{"ipv6", func(c *Cluster) { c.PodCIDRs.IPv6 = "fd00:10::/32" }, "podCIDRs.ipv6 fd00:10::/32 and serviceCIDRs.ipv6 fd00:10:96::/112 overlap"},
		{"ipv4", func(c *Cluster) { c.ServiceCIDRs.IPv4 = "10.96.0.0/11" }, "serviceCIDRs.ipv4 10.96.0.0/11 holds more than 2^20 addresses; use a prefix of /12 or longer"},
		{"ipv6", func(c *Cluster) { c.ServiceCIDRs.IPv6 = "fd00:10:96::/107" }, "serviceCIDRs.ipv6 fd00:10:96::/107 holds more than 2^20 addresses; use a prefix of /108 or longer"},
		{"ipv4", func(c *Cluster) { c.NodeCIDRMaskSizes.IPv4 = 16 }, "nodeCIDRMaskSizes.ipv4 16 must be longer than the prefix of podCIDRs.ipv4 10.244.0.0/16, by at most 16 bits"},
		{"ipv4", func(c *Cluster) { c.NodeCIDRMaskSizes.IPv4 = 33 }, "nodeCIDRMaskSizes.ipv4 33 must be longer"},
		{"ipv6", func(c *Cluster) { c.NodeCIDRMaskSizes.IPv6 = 73 }, "nodeCIDRMaskSizes.ipv6 73 must be longer than the prefix of podCIDRs.ipv6 fd00:10:244::/56, by at most 16 bits"},
		{"ipv6", func(c *Cluster) { c.NodeCIDRMaskSizes.IPv6 = 0 }, "nodeCIDRMaskSizes.ipv6 0 must be longer"},
		{"ipv4", func(c *Cluster) { c.DNSIPs.IPv4 = "dns" }, `dnsIPs.ipv4 "dns" is not an address in serviceCIDRs.ipv4 10.96.0.0/12`},
		{"ipv4", func(c *Cluster) { c.DNSIPs.IPv4 = "10.112.0.10" }, `dnsIPs.ipv4 "10.112.0.10" is not an address in serviceCIDRs.ipv4 10.96.0.0/12`},
		{"ipv6", func(c *Cluster) { c.DNSIPs.IPv6 = "10.96.0.10" }, `dnsIPs.ipv6 "10.96.0.10" is not an address in serviceCIDRs.ipv6 fd00:10:96::/112`},
		{"ipv4", func(c *Cluster) { c.DNSIPs.IPv4 = "10.96.0.1" }, "dnsIPs.ipv4 10.96.0.1 is the network address of serviceCIDRs.ipv4 10.96.0.0/12 or the kubernetes service's address"},
		{"ipv6", func(c *Cluster) { c.DNSIPs.IPv6 = "fd00:10:96::" }, "dnsIPs.ipv6 fd00:10:96:: is the network address"},
	} {
		bad := c
		bad.IPFamilies = []string{"ipv4", "ipv6"}
		tc.edit(&bad)
		if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Validate() = %v, want %q", err, tc.want)
		}
		// Only the entries of the cluster's families count.
		other := map[string]string{"ipv4": "ipv6", "ipv6": "ipv4"}[tc.family]
		bad.IPFamilies = []string{other}
		if err := bad.Validate(); err != nil {
			t.Errorf("an %s cluster refused the %s entries: %v", other, tc.family, err)
		}
	}
}

func TestClusterValidate(t *testing.T) {
	valid, err := ReadCluster(writeFile(t, clusterJSON))
	if err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(c *Cluster){
		"kind":         func(c *Cluster) { c.Kind = "etcd" },
		"endpoint":     func(c *Cluster) { c.Endpoint = "http://10.0.0.10:6443" },
		"domain":       func(c *Cluster) { c.Domain = "" },
		"validSubnets": func(c *Cluster) { c.NodeIP.ValidSubnets = []string{"10.0.0.0"} },
		"timeout":      func(c *Cluster) { c.NodeIP.Timeout = -1 },
		// These flags decide who may do what; chalkos sets them.
		"authorization-mode": func(c *Cluster) {
			c.ExtraArgs = map[string]map[string]string{"kube-apiserver": {"authorization-mode": "AlwaysAllow"}}
		},
		"anonymous-auth": func(c *Cluster) {
			c.ExtraArgs = map[string]map[string]string{"kube-apiserver": {"anonymous-auth": "true"}}
		},
		"authentication-config": func(c *Cluster) {
			c.ExtraArgs = map[string]map[string]string{"kube-apiserver": {"authentication-config": "/tmp/a.json"}}
		},
		"enable-bootstrap-token-auth": func(c *Cluster) {
			c.ExtraArgs = map[string]map[string]string{"kube-apiserver": {"enable-bootstrap-token-auth": "true"}}
		},
	} {
		c := valid
		edit(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestClusterFamiliesAndVIP(t *testing.T) {
	valid, err := ReadCluster(writeFile(t, clusterJSON))
	if err != nil {
		t.Fatal(err)
	}
	if families, err := valid.Families(); err != nil || len(families) != 1 || families[0] != nodeip.IPv4 {
		t.Errorf("Families() = %v, %v, want ipv4 alone", families, err)
	}
	dual := valid
	dual.IPFamilies = []string{"ipv6", "ipv4"}
	dual.VIP = VIP{Addresses: []string{"10.0.0.10", "fd00::10"}, Mode: "l2"}
	if err := dual.Validate(); err != nil {
		t.Fatalf("dual stack with a VIP per family: %v", err)
	}
	if families, _ := dual.Families(); len(families) != 2 || families[0] != nodeip.IPv6 {
		t.Errorf("Families() = %v, want ipv6 first", families)
	}
	named := valid
	named.Endpoint = "https://api.example.com:6443"
	named.VIP = VIP{Addresses: []string{"10.0.0.10"}, Mode: "l2"}
	if err := named.Validate(); err != nil {
		t.Errorf("a host name endpoint with a VIP: %v", err)
	}
	for name, edit := range map[string]func(c *Cluster){
		"unknown family":         func(c *Cluster) { c.IPFamilies = []string{"ipv5"} },
		"family twice":           func(c *Cluster) { c.IPFamilies = []string{"ipv4", "ipv4"} },
		"VIP not an address":     func(c *Cluster) { c.VIP = VIP{Addresses: []string{"10.0.0.10/32"}, Mode: "l2"} },
		"VIP of another family":  func(c *Cluster) { c.VIP = VIP{Addresses: []string{"10.0.0.10", "fd00::10"}, Mode: "l2"} },
		"two VIPs of one family": func(c *Cluster) { c.VIP = VIP{Addresses: []string{"10.0.0.10", "10.0.0.20"}, Mode: "l2"} },
		"endpoint not a VIP":     func(c *Cluster) { c.VIP = VIP{Addresses: []string{"10.0.0.20"}, Mode: "l2"} },
		"unknown mode":           func(c *Cluster) { c.VIP = VIP{Addresses: []string{"10.0.0.10"}, Mode: "bgp"} },
	} {
		c := valid
		edit(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNodeIPSelectorEndpointLast(t *testing.T) {
	c, err := ReadCluster(writeFile(t, clusterJSON))
	if err != nil {
		t.Fatal(err)
	}
	n, err := ParseNode([]byte(`{"kubernetes": {"nodeName": "cp1", "validSubnets": []}}`))
	if err != nil {
		t.Fatal(err)
	}
	pick := func(c Cluster, list ...string) string {
		t.Helper()
		sel, err := c.NodeIPSelector(n)
		if err != nil {
			t.Fatal(err)
		}
		var addrs []nodeip.Address
		for _, ip := range list {
			addrs = append(addrs, nodeip.Address{Interface: "eth0", IP: netip.MustParseAddr(ip)})
		}
		ips, err := sel.Select(addrs)
		if err != nil {
			return err.Error()
		}
		return ips[0].String()
	}
	// The endpoint 10.0.0.10 is a virtual address that sorts before the node's own one.
	if got := pick(c, "10.0.0.10", "10.0.0.11"); got != "10.0.0.11" {
		t.Errorf("node address and VIP: picked %s, want 10.0.0.11", got)
	}
	// A single control plane whose endpoint is its own address.
	if got := pick(c, "10.0.0.10"); got != "10.0.0.10" {
		t.Errorf("only the endpoint's address: picked %s, want 10.0.0.10", got)
	}
	v6 := c
	v6.IPFamilies = []string{"ipv6"}
	v6.Endpoint = "https://[fd00::10]:6443"
	if got := pick(v6, "fd00::10", "fd00::11"); got != "fd00::11" {
		t.Errorf("IPv6 endpoint: picked %s, want fd00::11", got)
	}
	// An endpoint named by a host name changes nothing.
	named := c
	named.Endpoint = "https://api.example.com:6443"
	if sel, err := named.NodeIPSelector(n); err != nil || len(sel.Last) != 0 {
		t.Errorf("host name endpoint: selector %+v, %v", sel, err)
	}
	if got := pick(named, "10.0.0.10", "10.0.0.11"); got != "10.0.0.10" {
		t.Errorf("host name endpoint: picked %s, want 10.0.0.10", got)
	}
}

func TestReadNode(t *testing.T) {
	n, err := ReadNode(writeFile(t, `{
	  "hostname": "cp1-host",
	  "network": {"networks": {"10-lan": {"address": ["10.0.0.11/24", "10.0.1.11/24"]}}},
	  "labels": {"zone": "a"},
	  "taints": [{"key": "dedicated", "value": "db", "effect": "NoSchedule"}],
	  "kubernetes": {"nodeName": "cp1", "nodeIPs": ["10.0.0.11"]}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "cp1" || n.Hostname != "cp1-host" || len(n.FixedIPs) != 1 || !n.FixedIPs[0].Equal(net.ParseIP("10.0.0.11")) || n.Labels["zone"] != "a" || len(n.Taints) != 1 {
		t.Errorf("node = %+v", n)
	}
	// The certificates name the addresses the node picked and its static addresses.
	n.IPs = []net.IP{net.ParseIP("10.0.0.11"), net.ParseIP("fd00::11")}
	var ips []string
	for _, ip := range n.CertificateIPs() {
		ips = append(ips, ip.String())
	}
	if strings.Join(ips, ",") != "10.0.0.11,fd00::11,10.0.1.11" {
		t.Errorf("CertificateIPs() = %v", ips)
	}

	if _, err := ReadNode(writeFile(t, `{"hostname": "n1", "kubernetes": null}`)); err == nil {
		t.Error("read a node without a Kubernetes identity")
	}
	if _, err := ReadNode(writeFile(t, `{"kubernetes": {"nodeName": "n1", "nodeIPs": ["nope"]}}`)); err == nil {
		t.Error("read a node with an invalid address")
	}
	if _, err := ReadNode(writeFile(t, `{"kubernetes": {"nodeName": "n1", "validSubnets": ["eth0"]}}`)); err == nil {
		t.Error("read a node with an invalid subnet")
	}
}

func TestNodeIPSelector(t *testing.T) {
	c, err := ReadCluster(writeFile(t, clusterJSON))
	if err != nil {
		t.Fatal(err)
	}
	node := func(identity string) Node {
		t.Helper()
		n, err := ParseNode([]byte(identity))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	addrs := []nodeip.Address{
		{Interface: "eth0", IP: netip.MustParseAddr("10.0.0.10")},
		{Interface: "eth0", IP: netip.MustParseAddr("10.0.0.11")},
		{Interface: "eth1", IP: netip.MustParseAddr("192.168.100.12")},
		{Interface: "eth2", IP: netip.MustParseAddr("10.244.0.5")},
	}
	// The VIPs sort last like the endpoint's address.
	withVIP := c
	withVIP.VIP = VIP{Addresses: []string{"10.0.0.10"}, Mode: "l2"}
	if sel, err := withVIP.NodeIPSelector(node(`{"kubernetes": {"nodeName": "n1"}}`)); err != nil || len(sel.Last) != 2 {
		t.Errorf("Last = %v, %v, want the endpoint's address and the VIP", sel.Last, err)
	}
	// A dual-stack node picks one address of each family.
	dual := c
	dual.IPFamilies = []string{"ipv4", "ipv6"}
	sel, err := dual.NodeIPSelector(node(`{"kubernetes": {"nodeName": "n1", "nodeIPs": ["192.168.100.12"], "validSubnets": ["fd00::/64"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := sel.String(); got != "ipv4 by nodeIP 192.168.100.12, ipv6 by validSubnets fd00::/64" {
		t.Errorf("dual stack: selector %s", got)
	}
	// The pod and service ranges of every family are never picked.
	if got := fmt.Sprint(sel.Reserved); got != "[10.244.0.0/16 fd00:10:244::/56 10.96.0.0/12 fd00:10:96::/112]" {
		t.Errorf("dual stack: reserved ranges %s", got)
	}
	if got, err := sel.Select(append(addrs, nodeip.Address{Interface: "eth1", IP: netip.MustParseAddr("fd00::12")})); err != nil || len(got) != 2 || got[1] != netip.MustParseAddr("fd00::12") {
		t.Errorf("dual stack: Select() = %v, %v", got, err)
	}
	// A fixed address of a family the cluster does not have is refused.
	if _, err := c.NodeIPSelector(node(`{"kubernetes": {"nodeName": "n1", "nodeIPs": ["fd00::12"]}}`)); err == nil {
		t.Error("accepted an ipv6 nodeIP in an ipv4 cluster")
	}

	for name, tc := range map[string]struct {
		node     Node
		selector string
		want     string
	}{
		"cluster's subnets": {node(`{"kubernetes": {"nodeName": "n1"}}`), "ipv4 by validSubnets 10.0.0.0/8, !10.0.0.10/32", "10.0.0.11"},
		"node's subnets":    {node(`{"kubernetes": {"nodeName": "n1", "validSubnets": ["192.168.100.0/24"]}}`), "ipv4 by validSubnets 192.168.100.0/24", "192.168.100.12"},
		"default filter":    {node(`{"kubernetes": {"nodeName": "n1", "validSubnets": []}}`), "ipv4 by the default filter", "10.0.0.11"},
		"fixed":             {node(`{"kubernetes": {"nodeName": "n1", "nodeIPs": ["192.168.100.12"]}}`), "ipv4 by nodeIP 192.168.100.12", "192.168.100.12"},
		// The pod range never holds the node's address.
		"pod range": {node(`{"kubernetes": {"nodeName": "n1", "validSubnets": ["10.244.0.0/16"]}}`), "ipv4 by validSubnets 10.244.0.0/16", ""},
	} {
		sel, err := c.NodeIPSelector(tc.node)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if sel.String() != tc.selector {
			t.Errorf("%s: selector %s, want %s", name, sel, tc.selector)
		}
		got, err := sel.Select(addrs)
		if tc.want == "" {
			if err == nil {
				t.Errorf("%s: picked %v", name, got)
			}
			continue
		}
		if err != nil || len(got) != 1 || got[0] != netip.MustParseAddr(tc.want) {
			t.Errorf("%s: Select() = %v, %v, want %s", name, got, err, tc.want)
		}
	}
}
