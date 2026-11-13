package chalkctl

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/api/router/v1/routerv1connect"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/layers"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/name, or writes it there with -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("the output differs from %s; run go test ./pkg/chalkctl -run %s -update and review the diff:\n%s", path, t.Name(), got)
	}
}

// cannedNode answers Status and Disks with fixed responses, and Info as the node s does, with the
// Kubernetes and router layers.
type cannedNode struct {
	nodev1connect.UnimplementedNodeServiceHandler
	s *chalkd.Server
}

func (n cannedNode) Info(ctx context.Context, req *connect.Request[nodev1.InfoRequest]) (*connect.Response[nodev1.InfoResponse], error) {
	resp, err := n.s.Info(ctx, req)
	if err == nil {
		resp.Msg.Layers = []string{layers.Kubernetes, layers.Router}
	}
	return resp, err
}

func (cannedNode) Status(context.Context, *connect.Request[nodev1.StatusRequest]) (*connect.Response[nodev1.StatusResponse], error) {
	return connect.NewResponse(cannedStatus()), nil
}

func (cannedNode) Disks(context.Context, *connect.Request[nodev1.DisksRequest]) (*connect.Response[nodev1.DisksResponse], error) {
	return connect.NewResponse(&nodev1.DisksResponse{Disks: []*nodev1.Disk{
		{Device: "/dev/vda", Size: 16 << 30, Type: "hdd", Model: "QEMU HARDDISK", Serial: "chalk-sys", Usage: "boot", Partitions: []*nodev1.Partition{
			{Device: "/dev/vda1", Size: 512 << 20, Label: "esp", Content: "vfat"},
			{Device: "/dev/vda2", Size: 1536 << 20, Label: "chalkos-a", Content: "erofs"},
		}},
		{Device: "/dev/nvme0n1", Size: 1000204886016, Type: "ssd", Model: "Samsung SSD 980", Serial: "S64DNF0R", Wwn: "eui.002538b1", Usage: "volume data"},
	}}), nil
}

// kubernetesStatus is the canned KubernetesService.Status.
type kubernetesStatus struct {
	kubernetesv1connect.UnimplementedKubernetesServiceHandler
}

func (kubernetesStatus) Status(context.Context, *connect.Request[kubernetesv1.StatusRequest]) (*connect.Response[kubernetesv1.StatusResponse], error) {
	return connect.NewResponse(&kubernetesv1.StatusResponse{
		Kind: "controlplane", State: "bootstrapped", NodeReady: "True", Vip: "holder", ControlPlane: "current",
		ApiServerVersion: "1.37.2", KubeletVersion: "1.37.1", ControlPlanePods: "kube-apiserver is not Ready",
		RevokedNodes: []*kubernetesv1.RevokedNode{
			{Name: "n7", RevokedAt: timestamppb.New(time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC))},
			{Name: "old-worker"},
		},
	}), nil
}

// routerStatus is the canned RouterService.Status.
type routerStatus struct {
	routerv1connect.UnimplementedRouterServiceHandler
}

func (routerStatus) Status(context.Context, *connect.Request[routerv1.StatusRequest]) (*connect.Response[routerv1.StatusResponse], error) {
	return connect.NewResponse(&routerv1.StatusResponse{
		Peers: []*routerv1.BGPPeerStatus{
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
	}), nil
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
	current, _ := json.Marshal(ta.identity(t))
	s, _ := installedNode(t, ta, current)
	addr := ta.startNode(t, s)
	mux := http.NewServeMux()
	n := cannedNode{s: s}
	mux.Handle(nodev1connect.NewNodeServiceHandler(n))
	mux.Handle(kubernetesv1connect.NewKubernetesServiceHandler(kubernetesStatus{}))
	mux.Handle(routerv1connect.NewRouterServiceHandler(routerStatus{}))
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
	golden(t, "status.golden", ta.stdout.String())
}

// TestDisksPrints holds what chalkctl disks prints of a node's disks.
func TestDisksPrints(t *testing.T) {
	ta := newTestApp(t)
	addr := cannedNodeAt(t, ta)
	if err := ta.run(context.Background(), ta.args([]string{"disks", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	golden(t, "disks.golden", ta.stdout.String())
}
