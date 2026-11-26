package chalkctl

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/timestamppb"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/api/router/v1/routerv1connect"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/internal/golden"
	"github.com/trevex/chalkos/pkg/layers"
)

// cannedNode answers Status and Disks with fixed responses, and Info as the node s does, with the
// Kubernetes and router layers.
type cannedNode struct {
	nodev1connect.UnimplementedNodeServiceHandler
	s *chalkd.Server
	// release is the chalkos release Info names; empty keeps the node's.
	release string
}

func (n cannedNode) Info(ctx context.Context, req *nodev1.InfoRequest) (*nodev1.InfoResponse, error) {
	resp, err := n.s.Info(ctx, req)
	if err == nil {
		resp.Layers = []string{layers.Kubernetes, layers.Router}
		resp.ModulePolicy = &nodev1.ModulePolicyStatus{State: nodev1.ModulePolicyState_MODULE_POLICY_STATE_ENFORCED, Version: "0.5.0"}
		resp.ChalkosVersion = cmp.Or(n.release, resp.ChalkosVersion)
		// What differs from one test node to the next.
		resp.Fingerprint, resp.Hostname, resp.BootDisk = strings.Repeat("f", 64), "n1", "/dev/vda"
	}
	return resp, err
}

func (cannedNode) Status(context.Context, *nodev1.StatusRequest) (*nodev1.StatusResponse, error) {
	return cannedStatus(), nil
}

func (cannedNode) Disks(context.Context, *nodev1.DisksRequest) (*nodev1.DisksResponse, error) {
	return &nodev1.DisksResponse{Disks: []*nodev1.Disk{
		{Device: "/dev/vda", Size: 16 << 30, Type: "hdd", Model: "QEMU HARDDISK", Serial: "chalk-sys", Usage: "boot", Partitions: []*nodev1.Partition{
			{Device: "/dev/vda1", Size: 512 << 20, Label: "esp", Content: "vfat"},
			{Device: "/dev/vda2", Size: 1536 << 20, Label: "chalkos-a", Content: "erofs"},
		}},
		{Device: "/dev/nvme0n1", Size: 1000204886016, Type: "ssd", Model: "Samsung SSD 980", Serial: "S64DNF0R", Wwn: "eui.002538b1", Usage: "volume data"},
	}}, nil
}

// kubernetesStatus is the canned KubernetesService.Status.
type kubernetesStatus struct {
	kubernetesv1connect.UnimplementedKubernetesServiceHandler
}

func (kubernetesStatus) Status(context.Context, *kubernetesv1.StatusRequest) (*kubernetesv1.StatusResponse, error) {
	return &kubernetesv1.StatusResponse{
		Kind: "controlplane", State: "bootstrapped", NodeReady: "True", Vip: "holder", ControlPlane: "current", ControlPlaneCurrent: true,
		ApiServerVersion: "1.37.2", KubeletVersion: "1.37.1", ControlPlanePods: "kube-apiserver is not Ready",
		RevokedNodes: []*kubernetesv1.RevokedNode{
			{Name: "n7", RevokedAt: timestamppb.New(time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC))},
			{Name: "old-worker"},
		},
	}, nil
}

// routerStatus is the canned RouterService.Status.
type routerStatus struct {
	routerv1connect.UnimplementedRouterServiceHandler
}

func (routerStatus) Status(context.Context, *routerv1.StatusRequest) (*routerv1.StatusResponse, error) {
	return &routerv1.StatusResponse{
		Peers: []*nodev1.BGPPeerStatus{
			{Name: "fabric", Neighbor: "eth1", State: "Established", UptimeSeconds: 3723, PrefixesReceived: 1, PrefixesSent: 2, Bfd: "up"},
			{Vrf: "tenant", Name: "transit", Neighbor: "203.0.113.254", State: "Active"},
		},
		Nat64:    []*routerv1.NAT64Status{{Name: "edge", Sessions: 3, Bindings: 2, Pools: []*routerv1.NAT64PoolStatus{{Protocol: "tcp", Size: 4535}, {Protocol: "udp", Size: 4535}, {Protocol: "icmp"}}}},
		Resolver: &routerv1.ResolverStatus{Running: true},
		VrfResolvers: []*routerv1.ResolverStatus{
			{Vrf: "guest", Error: "unbound@guest.service is inactive"},
		},
		Dhcp: []*routerv1.DHCPServerStatus{
			{Vrf: "default", Family: "ipv4", Running: true, Subnets: []*routerv1.DHCPSubnetStatus{{Name: "office", Subnet: "192.0.2.0/24", Leased: 2, Pool: 100}}},
			{Vrf: "default", Family: "ipv6", Running: true, Subnets: []*routerv1.DHCPSubnetStatus{{Name: "office6", Subnet: "2001:db8:1::/64", Leased: 1, Pool: math.MaxUint64, Delegated: 1, Delegable: 256}}},
			{Vrf: "guest", Family: "ipv4", Error: "dial unix /run/kea/dhcp4-guest.sock: connect: no such file or directory"},
		},
		DhcpRelays: []*routerv1.DHCPRelayStatus{{Vrf: "default", Running: true}, {Vrf: "guest", Error: "dhcrelay@guest.service is failed"}},
	}, nil
}

// cannedStatus is a node's status that reaches each line chalkctl status prints.
func cannedStatus() *nodev1.StatusResponse {
	running := &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0x81, 0xbd}, 16), Generation: 7, Revision: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", Tag: "v1.4.0"}
	return &nodev1.StatusResponse{
		ConfigVersion: "3fa9c1e2b7d04e5f",
		Trial: &nodev1.ConfigTrial{
			Version: "3fa9c1e2b7d04e5f", PreviousVersion: "77aa01bc23de45f6", Deadline: timestamppb.New(time.Date(2026, 11, 13, 15, 4, 0, 0, time.UTC)),
			IgnoreUnits: []string{"late.service"}, Error: "output demo failed",
		},
		TrialError:            "parse chalkd/trial: unexpected end of JSON input",
		PreviousConfigVersion: "77aa01bc23de45f6",
		StagedConfigVersion:   "77aa01bc23de45f6",
		PendingConfig:         &nodev1.PendingConfig{Version: "9c0ffee12345abcd", Since: timestamppb.New(time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC))},
		Platform:              "metal",
		Boot: &nodev1.BootStatus{
			Running: running, Entry: "chalkos_7+2-1.efi",
			Staged: &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0x3f, 0xa9}, 16), Generation: 8, Tag: "v1.5.0"}, StagedState: nodev1.SlotState_SLOT_STATE_STAGED,
			Previous: &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0x12, 0x34}, 16), Generation: 6, Revision: "0123456789abcdef0123-dirty"},
			Failed:   &nodev1.BootImage{RootHash: bytes.Repeat([]byte{0xde, 0xad}, 16), Generation: 9},
			Journal:  []string{"Nov 13 14:00:01 n1 chalkd[412]: health check failed", "Nov 13 14:00:02 n1 systemd[1]: Reached target Reboot."},
		},
		Volumes: []*nodev1.VolumeStatus{
			{Name: "var", Disk: "system", MountPoint: "/var", Present: true, Mounted: true},
			{Name: "data", Disk: "data", MountPoint: "/var/lib/data"},
		},
		Disks: []*nodev1.DiskStatus{
			{Name: "system", Device: "/dev/vda"},
			{Name: "data", Error: "no disk matches the selector serial=S64DNF0R"},
		},
		Outputs: []*nodev1.OutputStatus{
			{Name: "bird-conf", Hash: strings.Repeat("ab", 32)},
			{Name: "unbound-conf", Error: "its renderer failed (exit 2); see the node's journal"},
		},
		Certificates: []*nodev1.CertificateStatus{
			{Name: "node", NotAfter: timestamppb.New(time.Date(2027, 10, 8, 12, 0, 0, 0, time.UTC)), Problem: "expires in 20 days; renew it with chalkctl node renew <node>"},
			{Name: "Kubernetes share", Problem: "unreadable: parse the Kubernetes share"},
		},
		Trust: []*nodev1.TrustStatus{
			{Name: "OS CA", Fingerprints: []string{strings.Repeat("a", 64)}, Issuing: strings.Repeat("a", 64)},
			{Name: "Kubernetes CA", Fingerprints: []string{strings.Repeat("c", 64), strings.Repeat("d", 64)}, Issuing: strings.Repeat("d", 64)},
		},
		Time:        &nodev1.TimeStatus{Synchronised: true, Source: "10.0.2.2", OffsetSeconds: -0.0000123},
		FailedUnits: []string{"kea-dhcp4@guest.service"},
	}
}

// cannedNodeAt serves n1 with the canned answers and returns its address.
func cannedNodeAt(t *testing.T, ta *testApp) string {
	t.Helper()
	return cannedReleaseAt(t, ta, "")
}

// cannedReleaseAt serves n1 with the canned answers, its chalkd of the release, and returns its
// address.
func cannedReleaseAt(t *testing.T, ta *testApp, release string) string {
	t.Helper()
	s, _ := installedNode(t, ta, nil)
	addr := ta.startNode(t, s)
	mux := http.NewServeMux()
	n := cannedNode{s: s, release: release}
	server := connect.NewServer()
	nodev1connect.RegisterNodeServiceHandler(server, n)
	kubernetesv1connect.RegisterKubernetesServiceHandler(server, kubernetesStatus{})
	routerv1connect.RegisterRouterServiceHandler(server, routerStatus{})
	connecthttp.Mount(mux, server)

	canned := map[string]bool{
		nodev1connect.NodeServiceInfoProcedure:               true,
		nodev1connect.NodeServiceStatusProcedure:             true,
		nodev1connect.NodeServiceDisksProcedure:              true,
		kubernetesv1connect.KubernetesServiceStatusProcedure: true,
		routerv1connect.RouterServiceStatusProcedure:         true,
	}
	ta.setWrap(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if canned[r.URL.Path] {
				mux.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	return addr
}

// TestStatusPrints holds what chalkctl status prints of a node, the layers' sections and the
// inventory's comparisons included.
func TestStatusPrints(t *testing.T) {
	ta := newTestApp(t)
	addr := cannedNodeAt(t, ta)
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	golden.Check(t, "testdata/status.golden", ta.stdout.String())
}

// TestDisksPrints holds what chalkctl disks prints of a node's disks.
func TestDisksPrints(t *testing.T) {
	ta := newTestApp(t)
	addr := cannedNodeAt(t, ta)
	if err := ta.run(context.Background(), ta.args([]string{"disks", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	golden.Check(t, "testdata/disks.golden", ta.stdout.String())
}

// TestStatusJSON holds what chalkctl status -o json prints of a node: the summary's problems, the
// inventory's comparisons, and the node's and its layers' answers in protojson.
func TestStatusJSON(t *testing.T) {
	ta := newTestApp(t)
	addr := cannedNodeAt(t, ta)
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1", "-o", "json"}, addr)); err != nil {
		t.Fatal(err)
	}
	golden.Check(t, "testdata/status.json.golden", ta.stdout.String())
}

// TestClusterStatus prints a line of each node of the inventory, n2 unreachable among them, and
// with -o json each node's status.
func TestClusterStatus(t *testing.T) {
	ta := newTestApp(t)
	addr := cannedNodeAt(t, ta)
	writeFile(t, ta.inventory, testInventoryYAML+"    n2: {address: 10.0.0.12, files: [nodes/n2.yaml]}\n")
	data, err := os.ReadFile(filepath.Join(ta.dir, "nodes", "n1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ta.dir, "nodes", "n2.yaml"), string(data))
	// Nothing listens on port 1 of the loopback address.
	args := []string{"--inventory", ta.inventory, "--address", "n1=" + addr, "--address", "n2=127.0.0.1:1"}
	if err := ta.run(context.Background(), append([]string{"status"}, args...)); err != nil {
		t.Fatal(err)
	}
	golden.Check(t, "testdata/status-cluster.golden", ta.stdout.String())
	if !strings.HasPrefix(ta.stderr.String(), "chalkctl: n2 is unreachable: ") {
		t.Errorf("stderr = %q", ta.stderr)
	}
	ta.stdout.Reset()
	if err := ta.run(context.Background(), append([]string{"status", "-o", "json"}, args...)); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Nodes []statusJSON `json:"nodes"`
	}
	if err := json.Unmarshal(ta.stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Nodes) != 2 || out.Nodes[0].Node != "n1" || len(out.Nodes[0].Problems) != 18 || out.Nodes[0].Status == nil ||
		out.Nodes[1].Node != "n2" || out.Nodes[1].Error == "" || out.Nodes[1].Healthy || out.Nodes[1].Status != nil {
		t.Errorf("status -o json =\n%s", ta.stdout)
	}
}

// TestDisksJSON holds what chalkctl disks -o json prints: the node's Disks answer in protojson.
func TestDisksJSON(t *testing.T) {
	ta := newTestApp(t)
	addr := cannedNodeAt(t, ta)
	if err := ta.run(context.Background(), ta.args([]string{"disks", "n1", "-o", "json"}, addr)); err != nil {
		t.Fatal(err)
	}
	golden.Check(t, "testdata/disks.json.golden", ta.stdout.String())
}
