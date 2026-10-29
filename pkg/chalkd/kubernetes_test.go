package chalkd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	kubernetesplugin "github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes"
	"github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes/kubernetestest"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/effects"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/nodeconfig"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// kubernetesServer is an installed node of the kind with the files chalkd reads; with a share
// when share is set. Its control plane applies 19 objects at once; adjust changes the options
// its Kubernetes layer is built with.
func kubernetesServer(t *testing.T, kind string, share bool, adjust ...func(*kubernetesplugin.Options)) (*Server, *tooltest.Runner) {
	t.Helper()
	s, r := installedServer(t, section("", ""), false)
	addKubernetes(t, s, kind, share, adjust...)
	return s, r
}

// readsWrittenCluster has the Kubernetes layer read the cluster file the server's loader writes.
func readsWrittenCluster(o *kubernetesplugin.Options) { o.ClusterFile = o.Paths.Cluster }

// addKubernetes gives s the Kubernetes layer of a node of the kind with the files chalkd reads;
// with a share when share is set. adjust changes the options the layer is built with.
func addKubernetes(t *testing.T, s *Server, kind string, share bool, adjust ...func(*kubernetesplugin.Options)) *kubernetesplugin.Plugin {
	t.Helper()
	root := t.TempDir()
	paths := kubernetestest.Paths(root, s.Paths.StateDir)
	o := kubernetesplugin.Options{
		Paths: paths,
		// The image.json is the test's to write; the cluster file the server writes is not the one
		// the layer reads unless the test says so.
		ImageFile:   kubernetestest.ImageFile(paths),
		ClusterFile: kubernetestest.ClusterFile(root),
		Hooks: kubernetesplugin.Hooks{
			Slot:              s.Loader.Slot,
			Objects:           kubernetestest.NoObjects,
			ControlPlane:      kubernetestest.AppliedAtOnce,
			Node:              kubernetestest.NodeReady,
			EtcdEndpoints:     kubernetestest.NoEtcdEndpoints,
			ClusterAnswers:    kubernetestest.NoClusterAnswers,
			CheckControlPlane: kubernetestest.ControlPlaneCurrent,
			APIClient:         kubernetestest.EmptyAPIServer,
		},
	}
	for _, f := range adjust {
		f(&o)
	}
	k := kubernetesplugin.New(s, o)
	// The loops a test started would run on against its removed files, and load the tests after
	// it, until the test binary ends.
	t.Cleanup(k.Stop)
	withPlugins(t, s, layers.Of(layers.Kubernetes), k)
	addLayerEffects(t, s)
	prepareErrorHint(t, s)
	s.Loader.Layers = []nodeconfig.Layer{k}
	kubernetestest.WriteNode(t, paths, kind, share)
	return k
}

// kubernetesOf is the Kubernetes layer of s.
func kubernetesOf(s *Server) *kubernetesplugin.Plugin {
	for _, p := range s.Plugins.Compiled() {
		if k, ok := p.(*kubernetesplugin.Plugin); ok {
			return k
		}
	}
	return nil
}

// kubernetesPaths are the files the Kubernetes layer of s reads and writes.
func kubernetesPaths(s *Server) knode.Paths { return kubernetesOf(s).Paths() }

// imageFileOf is the image.json the Kubernetes layer of s reads.
func imageFileOf(s *Server) string { return kubernetestest.ImageFile(kubernetesPaths(s)) }

// kubernetesClient is the client of the Kubernetes layer over c.
func kubernetesClient(c *client.Conn) kubernetesv1connect.KubernetesServiceClient {
	return kubernetesv1connect.NewKubernetesServiceClient(c.HTTPClient(), c.BaseURL())
}

// kubernetesStatus is the state of s's Kubernetes as its layer serves it; nil on an image without
// the Kubernetes layer.
func kubernetesStatus(t *testing.T, s *Server) *kubernetesv1.StatusResponse {
	t.Helper()
	if !s.Plugins.Layers().Has(layers.Kubernetes) {
		return nil
	}
	return kubernetestest.Status(t, kubernetesOf(s))
}

func TestStatusKubernetes(t *testing.T) {
	waiting, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	bootstrapped, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	write(t, kubernetesPaths(bootstrapped).Bootstrapped(), "")
	noShare, _ := kubernetesServer(t, k8s.KindWorker, false)
	worker, _ := kubernetesServer(t, k8s.KindWorker, true, func(o *kubernetesplugin.Options) {
		o.Hooks.Node = func(context.Context) (*corev1.Node, error) { return nil, errors.New("connection refused") }
	})
	noAddress, _ := kubernetesServer(t, k8s.KindWorker, true)
	kubernetestest.WithoutAddress(t, kubernetesPaths(noAddress), "no node address matches the default filter (the node has no addresses)")
	preparing, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	kubernetestest.WithoutAddress(t, kubernetesPaths(preparing), "")
	otherKind, _ := kubernetesServer(t, k8s.KindWorker, false)
	kubernetestest.OtherKindsShare(t, kubernetesPaths(otherKind))
	noFirewall, _ := kubernetesServer(t, k8s.KindWorker, true)
	kubernetestest.WithoutFirewall(t, kubernetesPaths(noFirewall))
	// The preparation picked the address and still writes the certificates.
	writing, _ := kubernetesServer(t, k8s.KindWorker, true)
	if err := os.Remove(kubernetesPaths(writing).Prepared()); err != nil {
		t.Fatal(err)
	}
	// A dual-stack worker whose Node has a pod range of one family only, as when the control plane
	// runs with that family alone.
	dualStack := func(podCIDRs ...string) *Server {
		s, _ := kubernetesServer(t, k8s.KindWorker, true, func(o *kubernetesplugin.Options) {
			o.Hooks.Node = func(context.Context) (*corev1.Node, error) { return kubernetestest.ReadyNode(podCIDRs...), nil }
		})
		p := kubernetesPaths(s)
		data, err := os.ReadFile(p.Cluster)
		if err != nil {
			t.Fatal(err)
		}
		cluster := strings.NewReplacer(`"podCIDRs": {`, `"ipFamilies": ["ipv4", "ipv6"], "podCIDRs": {"ipv6": "fd00:10:244::/56", `,
			`"serviceCIDRs": {`, `"serviceCIDRs": {"ipv6": "fd00:10:96::/112", `, `"dnsIPs": {`, `"dnsIPs": {"ipv6": "fd00:10:96::a", `,
			`"nodeCIDRMaskSizes": {`, `"nodeCIDRMaskSizes": {"ipv6": 64, `).Replace(string(data))
		write(t, p.Cluster, cluster)
		return s
	}
	plain, _ := installedServer(t, section("", ""), false)
	for name, tc := range map[string]struct {
		s    *Server
		want *kubernetesv1.StatusResponse
	}{
		"waiting":               {waiting, &kubernetesv1.StatusResponse{Phase: kubernetesv1.Phase_PHASE_WAITING, Kind: "controlplane", State: "waiting for bootstrap or for the cluster at https://192.168.100.11:6443"}},
		"bootstrapped":          {bootstrapped, &kubernetesv1.StatusResponse{Phase: kubernetesv1.Phase_PHASE_BOOTSTRAPPED, Kind: "controlplane", State: "bootstrapped", NodeReady: "True"}},
		"no share":              {noShare, &kubernetesv1.StatusResponse{Phase: kubernetesv1.Phase_PHASE_PREPARING, Kind: "worker", State: "no share"}},
		"no address":            {noAddress, &kubernetesv1.StatusResponse{Phase: kubernetesv1.Phase_PHASE_PREPARING, Kind: "worker", State: "preparation failed: no node address matches the default filter (the node has no addresses)"}},
		"share of another kind": {otherKind, &kubernetesv1.StatusResponse{Phase: kubernetesv1.Phase_PHASE_PREPARING, Kind: "worker", State: "preparation failed: the node's share is for a controlplane node, but its config gives it the type worker"}},
		"firewall":              {noFirewall, &kubernetesv1.StatusResponse{Phase: kubernetesv1.Phase_PHASE_PREPARING, Kind: "worker", State: "preparation failed: accept VXLAN to the node's address: exit status 1"}},
		"preparing":             {preparing, &kubernetesv1.StatusResponse{Phase: kubernetesv1.Phase_PHASE_PREPARING, Kind: "controlplane", State: "preparing"}},
		"writing":               {writing, &kubernetesv1.StatusResponse{Phase: kubernetesv1.Phase_PHASE_PREPARING, Kind: "worker", State: "preparing"}},
		"worker":                {worker, &kubernetesv1.StatusResponse{Phase: kubernetesv1.Phase_PHASE_JOINED, Kind: "worker", State: "joined", NodeReady: "unknown: connection refused"}},
		"dual-stack worker":     {dualStack("10.244.1.0/24", "fd00:10:244:1::/64"), &kubernetesv1.StatusResponse{Phase: kubernetesv1.Phase_PHASE_JOINED, Kind: "worker", State: "joined", NodeReady: "True"}},
		"no pod range yet":      {dualStack(), &kubernetesv1.StatusResponse{Phase: kubernetesv1.Phase_PHASE_JOINED, Kind: "worker", State: "joined", NodeReady: "True"}},
		"no ipv6 pod range": {dualStack("10.244.1.0/24"), &kubernetesv1.StatusResponse{Phase: kubernetesv1.Phase_PHASE_JOINED, Kind: "worker", NodeReady: "True",
			State: "joined: the Node has no ipv6 pod range; the control plane allocates ranges of the families the cluster was created with"}},
		"no Kubernetes": {plain, nil},
	} {
		if _, err := tc.s.Status(context.Background(), connect.NewRequest(&nodev1.StatusRequest{})); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := kubernetesStatus(t, tc.s)
		if (got == nil) != (tc.want == nil) || got != nil && (got.Kind != tc.want.Kind || got.State != tc.want.State || got.Phase != tc.want.Phase || got.NodeReady != tc.want.NodeReady) {
			t.Errorf("%s: kubernetes status %v, want %v", name, got, tc.want)
		}
	}
}

// prepareErrorHint points the hint of the default image's kubernetes effect at the preparation's
// error file of s's Kubernetes side, as the image points it at the node's.
func prepareErrorHint(t *testing.T, s *Server) {
	t.Helper()
	var declared map[string]effects.Definition
	if err := json.Unmarshal([]byte(readFile(t, s.Loader.Effects)), &declared); err != nil {
		t.Fatal(err)
	}
	e, ok := declared["kubernetes"]
	if !ok || e.Hint == nil {
		t.Fatal("the default image's kubernetes effect has no hint")
	}
	e.Hint.File = kubernetesPaths(s).PrepareError()
	declared["kubernetes"] = e
	data, err := json.Marshal(declared)
	if err != nil {
		t.Fatal(err)
	}
	write(t, s.Loader.Effects, string(data))
}

// startKubernetes runs the loops of the Kubernetes layer of s, as chalkd starts them in normal
// mode, until the test ends.
func startKubernetes(t *testing.T, s *Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = kubernetesOf(s).Start(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}
