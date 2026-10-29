package chalkd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/anypb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/effects"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// kubernetesIdentity is identityWith for the Kubernetes node nodeName.
func kubernetesIdentity(nodeName string) string {
	return `{"hostname": "n1", "platform": "metal", "kubernetes": {"nodeName": "` + nodeName + `", "nodeIPs": ["192.168.100.11"]}, "storage": ` + section("", "") + `}`
}

// hostConfig is a Host document of the name and hostname.
func hostConfig(name, hostname string) string {
	return "---\napiVersion: chalkos.dev/v1alpha1\nkind: Host\nmetadata: {name: " + name + "}\nspec: {hostname: " + hostname + "}\n"
}

// clusterServer is a configured node of the kind with a share whose loader writes the cluster
// from the image facts and the delivered config into its Kubernetes side's cluster file.
func clusterServer(t *testing.T, kind string) (*Server, *tooltest.Runner) {
	t.Helper()
	s, r := configServer(t)
	addKubernetes(t, s, kind, true)
	s.Kubernetes.clusterFile = s.Kubernetes.Paths.Cluster
	writeFacts(t, s)
	kubernetesImage(t, s)
	return s, r
}

// withClusterSpec is workerConfig with the fields the YAML mapping spec gives added to its
// Cluster document's endpoint.
func withClusterSpec(spec string) string {
	return strings.Replace(workerConfig, `spec: {endpoint: "https://192.168.100.11:6443"}`,
		`spec: {endpoint: "https://192.168.100.11:6443", `+strings.TrimPrefix(spec, "{"), 1)
}

// loops records what deliveries ask of chalkd's own loops.
func loops(s *Server) *[]string {
	var acted []string
	s.actedOnSelf = func(a effects.ChalkdAction) {
		if a != "" {
			acted = append(acted, string(a))
		}
	}
	return &acted
}

func TestApplyConfigDeliversShare(t *testing.T) {
	s, r := kubernetesServer(t, k8s.KindWorker, false)
	share := testShare(t, k8s.KindWorker)
	// STATE keeps the share as Encode writes it, not as it was delivered.
	var delivered bytes.Buffer
	if err := json.Indent(&delivered, share, "", "  "); err != nil {
		t.Fatal(err)
	}
	resp, err := deliver(context.Background(), s, kubernetesIdentity("n1"), &nodev1.ApplyConfigRequest{Layers: shareLayers(t, delivered.Bytes())})
	if err != nil {
		t.Fatal(err)
	}
	path := s.Kubernetes.Paths.Share()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, share) {
		t.Fatalf("the recorded share is not the canonical encoding: %v", err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("share mode %v", info.Mode())
	}
	if !slices.Contains(r.Calls, "systemctl restart chalkos-kubernetes.service kubelet.service") {
		t.Errorf("commands %v", r.Calls)
	}
	if !slices.Contains(resp.Msg.RestartedUnits, "kubelet.service") {
		t.Errorf("restarted %v", resp.Msg.RestartedUnits)
	}
}

func TestApplyConfigRefusesShare(t *testing.T) {
	plain, _ := installedServer(t, section("", ""), false)
	worker, r := kubernetesServer(t, k8s.KindWorker, false)
	controlPlane, _ := kubernetesServer(t, k8s.KindControlPlane, false)
	other, err := pki.NewOSCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	otherNodeCA, err := pki.NewNodeCA(other, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	k, err := clustersecrets.NewKubernetesSecrets(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	foreignNodeCA, err := kpki.ControlPlaneShare(k, otherNodeCA).Encode()
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		s        *Server
		identity string
		share    []byte
		code     connect.Code
	}{
		"no Kubernetes":              {plain, kubernetesIdentity("n1"), testShare(t, k8s.KindWorker), connect.CodeInvalidArgument},
		"broken share":               {worker, kubernetesIdentity("n1"), []byte(`{"kind": "worker"}`), connect.CodeInvalidArgument},
		"another node's share":       {worker, kubernetesIdentity("n1"), testShareFor(t, k8s.KindWorker, "n2"), connect.CodeInvalidArgument},
		"no node name":               {worker, identityWith(section("", "")), testShare(t, k8s.KindWorker), connect.CodeInvalidArgument},
		"a node CA of another OS CA": {controlPlane, kubernetesIdentity("n1"), foreignNodeCA, connect.CodeInvalidArgument},
	} {
		_, err := deliver(context.Background(), tc.s, tc.identity, &nodev1.ApplyConfigRequest{Layers: shareLayers(t, tc.share)})
		if connect.CodeOf(err) != tc.code {
			t.Errorf("%s: %v, want %v", name, err, tc.code)
		}
	}
	if len(r.Calls) != 0 {
		t.Errorf("a refused share ran %v", r.Calls)
	}
	if _, err := os.Stat(worker.Kubernetes.Paths.Share()); err == nil {
		t.Error("a refused share was recorded")
	}
}

func TestApplyConfigRefusesShareOfAnotherKind(t *testing.T) {
	for image, share := range map[string]string{k8s.KindWorker: k8s.KindControlPlane, k8s.KindControlPlane: k8s.KindWorker} {
		s, r := kubernetesServer(t, image, false)
		before, _ := readIdentity(s)
		_, err := deliver(context.Background(), s, kubernetesIdentity("n1"), &nodev1.ApplyConfigRequest{Layers: shareLayers(t, testShare(t, share))})
		if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), share) {
			t.Errorf("%s share on a %s image: %v", share, image, err)
		}
		if len(r.Calls) != 0 {
			t.Errorf("%s share on a %s image ran %v", share, image, r.Calls)
		}
		if _, err := os.Stat(s.Kubernetes.Paths.Share()); err == nil {
			t.Errorf("%s share on a %s image was recorded", share, image)
		}
		if after, _ := readIdentity(s); !bytes.Equal(after, before) {
			t.Errorf("%s share on a %s image: the identity was recorded", share, image)
		}
	}
}

// A change of what the node's Kubernetes files are made from runs their preparation again,
// which picks the node's address and fills the firewall's VXLAN chain, and restarts the kubelet.
// The firewall itself is left alone, so a node whose firewall is disabled takes a new address.
// TestApplyConfigRestartsKubernetesOnChange: the kubelet and its preparation restart when what
// they read of the node's config changes, compared with the running slot: the Node's spec, the
// Host, the static addresses of the Network documents, the cluster the Cluster document gives,
// its endpoint among it, and the Flannel document. Other changes, of a Network beside its
// addresses, of the Node's name or of another kind, restart neither.
func TestApplyConfigRestartsKubernetesOnChange(t *testing.T) {
	base := workerConfig + networkConfig
	node := func(spec string) string {
		return strings.Replace(base, "spec: {type: worker, nodeIP: {addresses: [192.168.100.21]}}", "spec: "+spec, 1)
	}
	for name, c := range map[string]struct {
		config   string
		restarts bool
	}{
		"unchanged":           {base, false},
		"kubeletExtraArgs":    {node("{type: worker, nodeIP: {addresses: [192.168.100.21]}, kubeletExtraArgs: {max-pods: \"50\"}}"), true},
		"nodeIP.addresses":    {node("{type: worker, nodeIP: {addresses: [192.168.100.22]}}"), true},
		"nodeIP.validSubnets": {node("{type: worker, nodeIP: {addresses: [], validSubnets: [192.168.100.0/24]}}"), true},
		"the Node's name":     {strings.Replace(base, "kind: Node\nmetadata: {name: default}", "kind: Node\nmetadata: {name: node}", 1), false},
		"a Host":              {base + hostConfig("default", "h1"), true},
		"a static address":    {strings.Replace(base, "10.0.0.11/24", "10.0.0.12/24", 1), true},
		"the cluster":         {strings.Replace(base, `spec: {endpoint: "https://192.168.100.11:6443"}`, `spec: {endpoint: "https://192.168.100.11:6443", domain: lab.local}`, 1), true},
		"the endpoint":        {strings.Replace(base, "192.168.100.11:6443", "192.168.100.12:6443", 1), true},
		"a Flannel's sources": {base + "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Flannel\nmetadata: {name: default}\nspec: {vxlanSourceSubnets: [192.168.100.0/24]}\n", true},
		"a Network's gateway": {strings.Replace(base, "address: [10.0.0.11/24]", "address: [10.0.0.11/24], gateway: 10.0.0.1", 1), false},
		"a Demo":              {strings.Replace(base, "name: first", "name: second", 1), false},
	} {
		t.Run(name, func(t *testing.T) {
			s, r := clusterServer(t, k8s.KindWorker)
			if _, err := sendConfig(s, base); err != nil {
				t.Fatal(err)
			}
			r.ResetCalls()
			resp, err := sendConfig(s, c.config)
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(r.Calls, "systemctl restart chalkos-kubernetes.service kubelet.service"); got != c.restarts {
				t.Errorf("restarted %v, want %v: %v", got, c.restarts, r.Calls)
			}
			if restarted := slices.Contains(resp.RestartedUnits, "kubelet.service"); restarted != c.restarts {
				t.Errorf("restarted units %v", resp.RestartedUnits)
			}
		})
	}
}

// TestApplyConfigRestartsKubernetesOnHostChange: another hostname in the Host restarts the kubelet
// and its preparation, and the same Host under another name restarts neither.
func TestApplyConfigRestartsKubernetesOnHostChange(t *testing.T) {
	base := workerConfig + networkConfig
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, base); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		name, config string
		restarts     bool
	}{
		{"a Host", base + hostConfig("default", "h1"), true},
		{"another hostname", base + hostConfig("default", "h2"), true},
		{"another name", base + hostConfig("host", "h2"), false},
	} {
		r.ResetCalls()
		resp, err := sendConfig(s, step.config)
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(r.Calls, "systemctl restart chalkos-kubernetes.service kubelet.service"); got != step.restarts {
			t.Errorf("%s: restarted %v, want %v: %v", step.name, got, step.restarts, r.Calls)
		}
		if restarted := slices.Equal(resp.RestartedUnits, []string{"chalkos-kubernetes.service", "kubelet.service"}); restarted != step.restarts {
			t.Errorf("%s: restarted units %v", step.name, resp.RestartedUnits)
		}
	}
}

// A share delivered to a running control plane reaches its loop at once, which starts again
// with it.
func TestApplyConfigRestartsControlPlaneLoopWithNewShare(t *testing.T) {
	s, _ := kubernetesServer(t, k8s.KindControlPlane, true)
	k := s.Kubernetes
	// Only a reload restarts the loop this quickly.
	k.timing.RestartBackoff = time.Hour
	shares := make(chan kpki.Share, 4)
	k.hooks.ControlPlane = func(ctx context.Context, share kpki.Share, applied func(int)) error {
		shares <- share
		applied(19)
		<-ctx.Done()
		return ctx.Err()
	}
	if _, err := bootstrap(s, context.Background()); err != nil {
		t.Fatal(err)
	}
	<-shares
	replaced := testShare(t, k8s.KindControlPlane)
	want, err := kpki.ParseShare(replaced)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deliver(context.Background(), s, kubernetesIdentity("n1"), &nodev1.ApplyConfigRequest{Layers: shareLayers(t, replaced)}); err != nil {
		t.Fatal(err)
	}
	select {
	case second := <-shares:
		if second.CA.Certificate != want.CA.Certificate {
			t.Error("the loop started again with the old share")
		}
	case <-time.After(time.Minute):
		t.Fatal("the running loop did not start again with the delivered share")
	}
}

// A node that finds no address keeps the config, runs no kubelet, and says why.
func TestApplyConfigReportsMissingAddress(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	const reason = "no node address matches validSubnets 192.168.100.0/24 (the node has 10.0.2.15 on eth0)"
	r.Rules = append([]tooltest.Rule{{Prefix: "systemctl restart chalkos-kubernetes.service kubelet.service", Err: errors.New("exit status 1")}}, r.Rules...)
	withoutAddress(t, s, reason)
	_, err := sendConfig(s, workerConfig)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "the node runs no kubelet: preparation failed: "+reason) {
		t.Errorf("err = %v, want the missing address", err)
	}
}

// A restart that fails says why: the preparation's reason, or else the restart's own error, and
// names the units to restart by hand, since the config is stored.
func TestApplyConfigReportsFailedRestart(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	r.Rules = append([]tooltest.Rule{{Prefix: "systemctl restart chalkos-kubernetes.service kubelet.service", Err: errors.New("exit status 1")}}, r.Rules...)
	withoutFirewall(t, s)
	_, err := sendConfig(s, workerConfig)
	want := "restart chalkos-kubernetes.service kubelet.service: exit status 1; the node runs no kubelet: preparation failed: accept VXLAN to the node's address: exit status 1; the delivery is applied, so restart chalkos-kubernetes.service kubelet.service by hand"
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), want) {
		t.Errorf("a failed preparation: err = %v, want %s", err, want)
	}

	// The preparation succeeded; the restart failed for another reason.
	other, r := clusterServer(t, k8s.KindWorker)
	r.Rules = append([]tooltest.Rule{{Prefix: "systemctl restart chalkos-kubernetes.service kubelet.service", Err: errors.New("exit status 1")}}, r.Rules...)
	_, err = sendConfig(other, workerConfig)
	want = "restart chalkos-kubernetes.service kubelet.service: exit status 1; the delivery is applied, so restart chalkos-kubernetes.service kubelet.service by hand"
	if connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "preparation") {
		t.Errorf("failed restart: err = %v, want %s", err, want)
	}
}

// The preparation's reason is the Kubernetes units' alone: another unit whose restart fails on a
// node whose preparation failed says only its own error.
func TestApplyConfigReportsThePreparationForKubernetesUnitsAlone(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	withoutFirewall(t, s)
	r.Rules = append([]tooltest.Rule{{Prefix: "systemctl try-restart demo.service", Err: errors.New("exit status 1")}}, r.Rules...)
	_, err := sendConfig(s, strings.Replace(workerConfig, "name: first", "name: second", 1))
	want := "try-restart demo.service: exit status 1; the delivery is applied, so restart demo.service by hand"
	if connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "kubelet") {
		t.Errorf("err = %v, want %s", err, want)
	}
}

func TestApplyConfigRefusesInvalidAddressSettings(t *testing.T) {
	s, r := kubernetesServer(t, k8s.KindWorker, true)
	for name, kubernetes := range map[string]string{
		"nodeIPs":      `{"nodeName": "n1", "nodeIPs": ["192.168.100"]}`,
		"validSubnets": `{"nodeName": "n1", "nodeIPs": [], "validSubnets": ["192.168.100.0"]}`,
	} {
		identity := strings.Replace(kubernetesIdentity("n1"), `{"nodeName": "n1", "nodeIPs": ["192.168.100.11"]}`, kubernetes, 1)
		if _, err := apply(s, identity, ""); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v, want invalid argument", name, err)
		}
	}
	if len(r.Calls) != 0 {
		t.Errorf("a refused identity ran %v", r.Calls)
	}
}

// An identity's Kubernetes part is checked on an image without the Kubernetes layer too, as the
// config slots of other images, which an upgrade writes, may hold one.
func TestApplyConfigRefusesInvalidAddressSettingsWithoutTheLayer(t *testing.T) {
	s, r := installedServer(t, section("", ""), false)
	if s.Plugins.Layers().Has(layers.Kubernetes) {
		t.Fatal("the server's image has the Kubernetes layer")
	}
	identity := strings.Replace(kubernetesIdentity("n1"), `"nodeIPs": ["192.168.100.11"]`, `"nodeIPs": ["192.168.100"]`, 1)
	if _, err := apply(s, identity, ""); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "nodeIPs") {
		t.Errorf("err = %v, want the node IPs refused", err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("a refused identity ran %v", r.Calls)
	}
}
func TestApplyConfigRecordsNodeCAWithoutRestarts(t *testing.T) {
	s, r := kubernetesServer(t, k8s.KindControlPlane, true)
	p := s.Kubernetes.Paths
	current, err := knode.ReadShare(p)
	if err != nil {
		t.Fatal(err)
	}
	nodeCA := testNodeCA(t)
	next := current
	next.NodeCA = &nodeCA
	data, err := next.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Layers: shareLayers(t, data)})); err != nil {
		t.Fatal(err)
	}
	if got, err := knode.ReadShare(p); err != nil || *got.NodeCA != nodeCA {
		t.Errorf("the new node CA was not recorded: %v", err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("a new node CA ran %v", r.Calls)
	}

	// Any other change prepares the node's files again.
	other := testShare(t, k8s.KindControlPlane)
	if _, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Layers: shareLayers(t, other)})); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.Calls, "systemctl restart chalkos-kubernetes.service kubelet.service") {
		t.Errorf("a new share ran %v", r.Calls)
	}
}

// The same share delivered again changes no file the kubelet or chalkd's loops read, so nothing
// restarts and the loops are left alone: a delivery that changes nothing does nothing.
func TestApplyConfigTheSameShareAgainDoesNothing(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	share := []byte(readFile(t, filepath.Join(s.Paths.StateDir, "kubernetes", "share.json")))
	acted := loops(s)
	r.Calls = nil
	resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Layers: shareLayers(t, share)}))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Msg.RestartedUnits) != 0 || len(r.Calls) != 0 || len(*acted) != 0 {
		t.Errorf("restarted %v, ran %v, acted on the loops %v", resp.Msg.RestartedUnits, r.Calls, *acted)
	}
}

// A share that differs in its node CA alone, delivered with config, restarts the kubelet when the
// config changes what it reads, and only then; chalkd's loops are left alone.
func TestApplyConfigNodeCAWithConfig(t *testing.T) {
	s, r := clusterServer(t, k8s.KindControlPlane)
	controlPlane := strings.Replace(workerConfig, "type: worker", "type: controlplane", 1)
	if _, err := sendConfig(s, controlPlane); err != nil {
		t.Fatal(err)
	}
	withNodeCA := func() []byte {
		current, err := knode.ReadShare(s.Kubernetes.Paths)
		if err != nil {
			t.Fatal(err)
		}
		nodeCA := testNodeCA(t)
		current.NodeCA = &nodeCA
		data, err := current.Encode()
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	acted := loops(s)
	for _, step := range []struct {
		name, config string
		want         []string
	}{
		{"the Node's subnets", strings.Replace(controlPlane, "nodeIP: {addresses: [192.168.100.21]}}", "nodeIP: {addresses: [192.168.100.21], validSubnets: [192.168.100.0/24]}}", 1), []string{"chalkos-controlplane.service", "chalkos-kubernetes.service", "kubelet.service"}},
		{"a Demo", strings.NewReplacer("nodeIP: {addresses: [192.168.100.21]}}", "nodeIP: {addresses: [192.168.100.21], validSubnets: [192.168.100.0/24]}}", "name: first", "name: second").Replace(controlPlane), []string{"demo.service"}},
	} {
		r.Calls, *acted = nil, nil
		resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: step.config, Layers: shareLayers(t, withNodeCA())}))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(resp.Msg.RestartedUnits, step.want) || len(*acted) != 0 {
			t.Errorf("%s: restarted %v, acted on the loops %v; want %v", step.name, resp.Msg.RestartedUnits, *acted, step.want)
		}
	}
}

func TestApplyConfigWritesTheClusterAndRestartsKubernetes(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	c, err := k8s.ReadCluster(s.Kubernetes.Paths.Cluster)
	if err != nil || c.Domain != "cluster.local" || c.Images.KubeProxy != "p" {
		t.Fatalf("cluster = %+v, %v", c, err)
	}

	// The same config changes no cluster and restarts nothing.
	r.Calls = nil
	resp, err := sendConfig(s, workerConfig)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(resp.RestartedUnits, "kubelet.service") {
		t.Errorf("restarted %v for the same cluster", resp.RestartedUnits)
	}

	r.Calls = nil
	if resp, err = sendConfig(s, withClusterSpec("{domain: lab.local}")); err != nil {
		t.Fatal(err)
	}
	if c, _ := k8s.ReadCluster(s.Kubernetes.Paths.Cluster); c.Domain != "lab.local" {
		t.Errorf("domain = %q", c.Domain)
	}
	if !slices.Contains(resp.RestartedUnits, "kubelet.service") || !slices.Contains(r.Calls, "systemctl restart chalkos-kubernetes.service kubelet.service") {
		t.Errorf("restarted %v, ran %v", resp.RestartedUnits, r.Calls)
	}
}

// A restart that failed once the config was stored names the units to restart by hand. The
// effects compare a retry with the config and facts from before the delivery that
// failed, so the retry restarts them, although the slot and the cluster file are the delivered
// ones already.
func TestApplyConfigNamesAFailedClusterRestart(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	next := withClusterSpec("{domain: lab.local}")
	r.Rules = []tooltest.Rule{{Prefix: "systemctl restart chalkos-kubernetes.service kubelet.service", Err: errors.New("exit status 1")}}
	if _, err := sendConfig(s, next); err == nil || !strings.Contains(err.Error(), "restart chalkos-kubernetes.service kubelet.service by hand once that is fixed, or deliver it again") {
		t.Fatalf("err = %v, want the units named", err)
	}
	if c, _ := k8s.ReadCluster(s.Kubernetes.Paths.Cluster); c.Domain != "lab.local" {
		t.Fatalf("domain = %q, want the cluster file written", c.Domain)
	}
	r.Rules, r.Calls = nil, nil
	resp, err := sendConfig(s, next)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.RestartedUnits, []string{"chalkos-kubernetes.service", "kubelet.service"}) || !slices.Contains(r.Calls, "systemctl restart chalkos-kubernetes.service kubelet.service") {
		t.Errorf("the retry restarted %v, ran %v", resp.RestartedUnits, r.Calls)
	}
	// Applied whole, the delivery leaves no baseline: the next one compares with the slot.
	if _, err := os.Stat(s.Paths.EffectsBaseline); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("baseline after a delivery applied whole: %v", err)
	}
	r.Calls = nil
	if resp, err = sendConfig(s, next); err != nil || len(resp.RestartedUnits) != 0 {
		t.Errorf("the same config again restarted %v: %v", resp.GetRestartedUnits(), err)
	}
}

// A share delivered with the same config, whose slot cannot be written once the share is
// recorded, restarts nothing but chalkd's loops, which read the share. Its retry compares with the
// share from before, so it restarts the kubelet and its preparation and reloads the loops again,
// although STATE holds the delivered share already.
func TestApplyConfigRetriesAShareWhoseSlotFailed(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	acted := loops(s)
	share := testShare(t, k8s.KindWorker)
	send := func() (*nodev1.ApplyConfigResponse, error) {
		resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: workerConfig, Layers: shareLayers(t, share)}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	slots := filepath.Dir(slotOf(t, s))
	if err := os.Chmod(slots, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(slots, 0o755) })
	r.Calls = nil
	if _, err := send(); err == nil || !strings.Contains(err.Error(), "store the config") {
		t.Fatalf("err = %v, want the slot's failure", err)
	}
	if recorded := readFile(t, filepath.Join(s.Paths.StateDir, "kubernetes", "share.json")); !strings.Contains(recorded, "BEGIN CERTIFICATE") {
		t.Fatalf("share.json = %s, want the delivered share recorded", recorded)
	}
	if slices.Contains(r.Calls, "systemctl restart chalkos-kubernetes.service kubelet.service") || !slices.Equal(*acted, []string{"reload"}) {
		t.Errorf("ran %v, acted on the loops %v; want the loops reloaded alone", r.Calls, *acted)
	}
	if err := os.Chmod(slots, 0o755); err != nil {
		t.Fatal(err)
	}
	r.Calls, *acted = nil, nil
	resp, err := send()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.RestartedUnits, []string{"chalkos-kubernetes.service", "kubelet.service"}) || !slices.Equal(*acted, []string{"reload"}) {
		t.Errorf("the retry restarted %v, acted on the loops %v", resp.RestartedUnits, *acted)
	}
}

// A share delivered alone whose kubelet restart fails restarts the kubelet when it is delivered
// again. The baseline the retry compares with holds the share's kind and the hashes of its other
// keys, readable by root alone, and none of its certificates or keys.
func TestApplyConfigRetriesAShareWhoseRestartFailed(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	share := testShare(t, k8s.KindWorker)
	send := func() (*nodev1.ApplyConfigResponse, error) {
		resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Layers: shareLayers(t, share)}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	r.Rules = []tooltest.Rule{{Prefix: "systemctl restart chalkos-kubernetes.service kubelet.service", Err: errors.New("exit status 1")}}
	if _, err := send(); err == nil || !strings.Contains(err.Error(), "restart chalkos-kubernetes.service kubelet.service by hand") {
		t.Fatalf("err = %v, want the units named", err)
	}
	info, err := os.Stat(s.Paths.EffectsBaseline)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("baseline: %v, %v; want it readable by root alone", info, err)
	}
	if b := readFile(t, s.Paths.EffectsBaseline); strings.Contains(b, "BEGIN") || strings.Contains(b, "s3cret") || !strings.Contains(b, `"kind":"worker"`) {
		t.Errorf("baseline = %s, want the kind and hashes alone", b)
	}
	r.Rules, r.Calls = nil, nil
	resp, err := send()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.RestartedUnits, []string{"chalkos-kubernetes.service", "kubelet.service"}) {
		t.Errorf("the retry restarted %v, ran %v", resp.RestartedUnits, r.Calls)
	}
}

// A slot whose documents cannot be written for the granted units leaves chalkd's loops acted on
// all the same, since what they read is in place, and its retry acts on them again.
func TestApplyConfigActsOnTheLoopsWhenTheGrantedConfigFails(t *testing.T) {
	s, _ := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	acted := loops(s)
	// A file where demo's credentials go.
	credentials := filepath.Join(s.Loader.RunDir, "credentials", "demo")
	if err := os.RemoveAll(credentials); err != nil {
		t.Fatal(err)
	}
	write(t, credentials, "")
	next := withClusterSpec("{domain: lab.local}")
	if _, err := sendConfig(s, next); err == nil || !strings.Contains(err.Error(), "write the config for the granted units") {
		t.Fatalf("err = %v, want the granted units' config to fail", err)
	}
	if !slices.Equal(*acted, []string{"restart"}) {
		t.Errorf("acted on the loops %v, want them restarted for the new cluster", *acted)
	}
	if err := os.Remove(credentials); err != nil {
		t.Fatal(err)
	}
	*acted = nil
	if _, err := sendConfig(s, next); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*acted, []string{"restart"}) {
		t.Errorf("the retry acted on the loops %v", *acted)
	}
}

// A delivery that fails once it wrote the cluster file, here to record its node certificate,
// restarts chalkd's loops all the same, which read the new cluster, and its retry restarts them
// again.
func TestApplyConfigActsOnTheLoopsWhenTheNodeCertificateFails(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	nodeCA := withNodeCertificate(t, s)
	cert, err := pki.IssueNode(nodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	acted := loops(s)
	send := func() (*nodev1.ApplyConfigResponse, error) {
		resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{
			Config: withClusterSpec("{domain: lab.local}"), NodeCertificate: []byte(cert.Certificate), NodeKey: []byte(cert.Key),
		}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	dir := filepath.Join(s.Paths.StateDir, "chalkd")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if _, err := send(); err == nil || !strings.Contains(err.Error(), "record the node certificate") {
		t.Fatalf("err = %v, want the node certificate's failure", err)
	}
	if c, _ := k8s.ReadCluster(s.Kubernetes.Paths.Cluster); c.Domain != "lab.local" {
		t.Fatalf("domain = %q, want the cluster file written", c.Domain)
	}
	if !slices.Equal(*acted, []string{"restart"}) {
		t.Errorf("acted on the loops %v, want them restarted for the new cluster", *acted)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r.Calls, *acted = nil, nil
	resp, err := send()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.RestartedUnits, []string{"chalkos-kubernetes.service", "kubelet.service"}) || !slices.Equal(*acted, []string{"restart"}) {
		t.Errorf("the retry restarted %v, acted on the loops %v", resp.RestartedUnits, *acted)
	}
}

// A share delivered alone after a slot whose documents could not be written for the granted
// units writes them before it restarts the units the failed delivery changed, and keeps the
// baseline while it cannot; once it is applied whole, a retry of the config restarts nothing.
func TestApplyConfigWritesTheGrantedConfigForAShareAfterAFailure(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	// A file where demo's credentials go.
	credentials := filepath.Join(s.Loader.RunDir, "credentials", "demo")
	if err := os.RemoveAll(credentials); err != nil {
		t.Fatal(err)
	}
	write(t, credentials, "")
	next := strings.Replace(workerConfig, "token: s3cret", "token: n3w", 1)
	if _, err := sendConfig(s, next); err == nil || !strings.Contains(err.Error(), "write the config for the granted units") {
		t.Fatalf("err = %v, want the granted units' config to fail", err)
	}
	share := testShare(t, k8s.KindWorker)
	send := func() (*nodev1.ApplyConfigResponse, error) {
		resp, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Layers: shareLayers(t, share)}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	r.Calls = nil
	if _, err := send(); err == nil || !strings.Contains(err.Error(), "write the config for the granted units") {
		t.Fatalf("err = %v, want the granted units' config to fail again", err)
	}
	if slices.Contains(r.Calls, "systemctl try-restart demo.service") {
		t.Errorf("ran %v, want demo left alone on the files from before", r.Calls)
	}
	if _, err := os.Stat(s.Paths.EffectsBaseline); err != nil {
		t.Fatalf("baseline after a share that failed: %v", err)
	}
	if err := os.Remove(credentials); err != nil {
		t.Fatal(err)
	}
	var credential string
	r.Before = func(line string) {
		if line == "systemctl try-restart demo.service" {
			b, _ := os.ReadFile(filepath.Join(credentials, "demo.token"))
			credential = string(b)
		}
	}
	resp, err := send()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.RestartedUnits, []string{"chalkos-kubernetes.service", "demo.service", "kubelet.service"}) || credential != "n3w" {
		t.Errorf("restarted %v, demo's credential %q when it restarted; want demo on the new one", resp.RestartedUnits, credential)
	}
	if _, err := os.Stat(s.Paths.EffectsBaseline); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("baseline after a delivery applied whole: %v", err)
	}
	r.Before = nil
	if resp, err = sendConfig(s, next); err != nil || len(resp.RestartedUnits) != 0 {
		t.Errorf("the retry of the config restarted %v: %v", resp.GetRestartedUnits(), err)
	}
}

// A cluster file that differs from the one the running slot's config gives, as one a boot whose
// config loader failed left missing, restarts the kubelet too.
func TestApplyConfigRestartsForAClusterFileThatDiffers(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.Kubernetes.Paths.Cluster); err != nil {
		t.Fatal(err)
	}
	r.Calls = nil
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.Calls, "systemctl restart chalkos-kubernetes.service kubelet.service") {
		t.Errorf("ran %v", r.Calls)
	}
}

// The settings a cluster's certificates, etcd and allocated ranges were made for are refused,
// also when the node's cluster file is missing, as after a boot whose loader failed.
func TestApplyConfigRefusesChangedFixedSettings(t *testing.T) {
	for field, spec := range map[string]string{
		"ipFamilies":   "{ipFamilies: [ipv4, ipv6]}",
		"podCIDRs":     "{podCIDRs: {ipv4: 10.245.0.0/16}}",
		"serviceCIDRs": "{serviceCIDRs: {ipv4: 10.97.0.0/16}}",
		"dnsIPs":       "{dnsIPs: {ipv4: 10.96.0.53}}",
	} {
		t.Run(field, func(t *testing.T) {
			s, r := clusterServer(t, k8s.KindWorker)
			if _, err := sendConfig(s, workerConfig); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(s.Kubernetes.Paths.Cluster); err != nil {
				t.Fatal(err)
			}
			r.Calls = nil
			_, err := sendConfig(s, withClusterSpec(spec))
			if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "cluster's "+field+" from") {
				t.Fatalf("err = %v", err)
			}
			if len(r.Calls) != 0 {
				t.Errorf("ran %v", r.Calls)
			}
			if exists(s.Kubernetes.Paths.Cluster) {
				t.Error("wrote the refused cluster")
			}
		})
	}
}

// A node holding a share keeps its type: its certificates are those of its kind, and a control
// plane's etcd data stays its own. The type is compared with the share the node holds, also when
// the stored config gives no cluster, and a share of the new type delivered with the config does
// not change it. A node without a share takes any type.
func TestApplyConfigRefusesAnotherNodeType(t *testing.T) {
	controlPlane := strings.Replace(workerConfig, "type: worker", "type: controlplane", 1)
	const refused = `the node's type from "worker" to "controlplane"`
	for name, tc := range map[string]struct {
		share, stored bool
		delivered     []byte
		want          string
	}{
		"a stored cluster":                 {true, true, nil, refused},
		"no stored cluster":                {true, false, nil, refused},
		"a share of the new type with it":  {true, true, testShare(t, k8s.KindControlPlane), refused},
		"no share":                         {false, true, nil, ""},
		"the first config without a share": {false, false, nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			s, r := configServer(t)
			addKubernetes(t, s, k8s.KindWorker, tc.share)
			s.Kubernetes.clusterFile = s.Kubernetes.Paths.Cluster
			kubernetesImage(t, s)
			if tc.stored {
				if _, err := sendConfig(s, workerConfig); err != nil {
					t.Fatal(err)
				}
			}
			r.Calls = nil
			_, err := s.ApplyConfig(context.Background(), connect.NewRequest(&nodev1.ApplyConfigRequest{Config: controlPlane, Layers: shareLayers(t, tc.delivered)}))
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if c, err := k8s.ReadCluster(s.Kubernetes.Paths.Cluster); err != nil || c.Kind != k8s.KindControlPlane {
					t.Errorf("cluster = %+v, %v", c, err)
				}
				return
			}
			if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
			if len(r.Calls) != 0 {
				t.Errorf("ran %v", r.Calls)
			}
			if c, err := k8s.ReadCluster(s.Kubernetes.Paths.Cluster); err != nil || c.Kind != k8s.KindWorker {
				t.Errorf("cluster = %+v, %v; want the worker's kept", c, err)
			}
		})
	}
}

// The kubelet applies the node's labels and taints, and the control-plane taint
// allowSchedulingOnControlPlanes leaves off, only when it registers the node, so a node holding a
// share refuses changes to them. The first Node and Cluster documents, unchanged values and a node
// without a share take any.
func TestApplyConfigRefusesChangedRegistrationFields(t *testing.T) {
	node := func(spec string) string {
		return strings.Replace(workerConfig, "nodeIP: {addresses: [192.168.100.21]}}", "nodeIP: {addresses: [192.168.100.21]}, "+spec+"}", 1)
	}
	labelled := node("labels: {node.kubernetes.io/storage: ssd}")
	tainted := node("taints: [{key: dedicated, effect: NoSchedule}]")
	for name, tc := range map[string]struct {
		share         bool
		before, after string
		want          []string
	}{
		"labels added":    {true, workerConfig, labelled, []string{`Node "default" spec.labels: `, "kubectl label"}},
		"labels changed":  {true, labelled, node("labels: {node.kubernetes.io/storage: hdd}"), []string{`Node "default" spec.labels: `, "kubectl label"}},
		"labels removed":  {true, labelled, workerConfig, []string{`Node "default" spec.labels: `}},
		"taints added":    {true, workerConfig, tainted, []string{`Node "default" spec.taints: `, "kubectl taint"}},
		"taints removed":  {true, tainted, workerConfig, []string{`Node "default" spec.taints: `}},
		"scheduling":      {true, workerConfig, withClusterSpec("{allowSchedulingOnControlPlanes: true}"), []string{`Cluster "default" spec.allowSchedulingOnControlPlanes: `, "kubectl taint"}},
		"unchanged":       {true, labelled, labelled, nil},
		"the first Node":  {true, "", labelled, nil},
		"no share":        {false, workerConfig, labelled, nil},
		"no share, taint": {false, workerConfig, tainted, nil},
	} {
		t.Run(name, func(t *testing.T) {
			s, r := configServer(t)
			addKubernetes(t, s, k8s.KindWorker, tc.share)
			s.Kubernetes.clusterFile = s.Kubernetes.Paths.Cluster
			writeFacts(t, s)
			kubernetesImage(t, s)
			if tc.before != "" {
				if _, err := sendConfig(s, tc.before); err != nil {
					t.Fatal(err)
				}
			}
			r.Calls = nil
			_, err := sendConfig(s, tc.after)
			if tc.want == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("err = %v", err)
			}
			for _, want := range append(tc.want, "applies when the node registers with the cluster", "or reinstall the node") {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want %q", err, want)
				}
			}
			if len(r.Calls) != 0 {
				t.Errorf("ran %v", r.Calls)
			}
		})
	}
}

// A changed cluster starts a control plane's loop again once the one before ended.
func TestApplyConfigRestartsTheControlPlaneLoop(t *testing.T) {
	s, _ := clusterServer(t, k8s.KindControlPlane)
	k := s.Kubernetes
	p := k.Paths
	ips, err := knode.ReadNodeIPs(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := knode.WritePin(p, ips); err != nil {
		t.Fatal(err)
	}
	write(t, p.Bootstrapped(), "")
	var mu sync.Mutex
	var events []string
	record := func(e string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}
	seen := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(events)
	}
	k.hooks.ControlPlane = func(ctx context.Context, _ kpki.Share, applied func(int)) error {
		record("start")
		applied(1)
		<-ctx.Done()
		// A loop that takes a while to end must have ended before the next starts.
		time.Sleep(50 * time.Millisecond)
		record("end")
		return ctx.Err()
	}
	k.Start()
	eventually(t, "the first loop", func() bool { return len(seen()) == 1 })

	config := strings.Replace(workerConfig, "type: worker", "type: controlplane", 1)
	if _, err := sendConfig(s, config); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the loop after the first config", func() bool { return len(seen()) == 3 })
	if _, err := sendConfig(s, strings.Replace(config, `:6443"}`, `:6443", domain: lab.local}`, 1)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the loop after the changed cluster", func() bool { return len(seen()) == 5 })
	if got := seen(); !slices.Equal(got, []string{"start", "end", "start", "end", "start"}) {
		t.Errorf("events = %v", got)
	}
}

// Without a stored cluster, as on a node whose stored config holds no Cluster document, the
// fixed settings have nothing to compare with.
func TestApplyConfigFirstClusterIsNotCompared(t *testing.T) {
	s, _ := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, withClusterSpec("{podCIDRs: {ipv4: 10.245.0.0/16}}")); err != nil {
		t.Fatal(err)
	}
}

// Ranges and addresses compare as what they denote, not as they are spelt.
func TestApplyConfigComparesRangesCanonically(t *testing.T) {
	s, _ := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, withClusterSpec(`{ipFamilies: [ipv4, ipv6], serviceCIDRs: {ipv4: 10.96.0.0/12, ipv6: "fd00:10:96::/108"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := sendConfig(s, withClusterSpec(`{ipFamilies: [ipv4, ipv6], serviceCIDRs: {ipv4: 10.96.0.0/12, ipv6: "fd00:10:96:0::/108"}, dnsIPs: {ipv6: "fd00:10:96:0::a"}}`)); err != nil {
		t.Fatal(err)
	}
	_, err := sendConfig(s, withClusterSpec(`{ipFamilies: [ipv4, ipv6], serviceCIDRs: {ipv4: 10.96.0.0/12, ipv6: "fd00:10:97::/108"}}`))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "serviceCIDRs") {
		t.Errorf("another range: err = %v", err)
	}
}

// A control plane applies the objects of changed Addon documents at once, without restarting
// a unit, from the config stored by then.
func TestApplyConfigAppliesChangedAddons(t *testing.T) {
	s, r := clusterServer(t, k8s.KindControlPlane)
	k := s.Kubernetes
	p := k.Paths
	ips, err := knode.ReadNodeIPs(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := knode.WritePin(p, ips); err != nil {
		t.Fatal(err)
	}
	write(t, p.Bootstrapped(), "")
	k.hooks.Slot = s.Loader.Slot
	k.hooks.Objects = k.objects
	applied := make(chan string, 10)
	k.hooks.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) error {
		c, err := k8s.ReadCluster(p.Cluster)
		if err != nil {
			return err
		}
		objects, err := k.hooks.Objects(c)
		if err != nil {
			return err
		}
		last, _ := objects[len(objects)-1]["metadata"].(map[string]any)
		applied <- fmt.Sprint(last["name"])
		<-ctx.Done()
		return ctx.Err()
	}
	config := strings.Replace(workerConfig, "type: worker", "type: controlplane", 1)
	if _, err := sendConfig(s, config); err != nil {
		t.Fatal(err)
	}
	if got := <-applied; got != "kube-dns" {
		t.Errorf("applied up to %s, want chalkos's add-ons alone", got)
	}

	r.Calls = nil
	resp, err := sendConfig(s, config+`---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Addon
metadata: {name: apps}
spec: {objects: [{apiVersion: v1, kind: Namespace, metadata: {name: apps}}]}
`)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-applied:
		if got != "apps" {
			t.Errorf("applied up to %s, want the Addon's namespace", got)
		}
	case <-time.After(time.Minute):
		t.Fatal("the changed Addon documents were not applied")
	}
	if len(resp.RestartedUnits) != 0 || slices.ContainsFunc(r.Calls, func(c string) bool { return strings.HasPrefix(c, "systemctl restart") }) {
		t.Errorf("restarted %v, ran %v", resp.RestartedUnits, r.Calls)
	}
}

// A worker applies no Addon documents: chalkctl delivers none to it, and a changed one it is
// given anyway restarts no unit.
func TestApplyConfigOnAWorkerKeepsChangedAddons(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	if _, err := sendConfig(s, workerConfig); err != nil {
		t.Fatal(err)
	}
	r.Calls = nil
	resp, err := sendConfig(s, workerConfig+`---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Addon
metadata: {name: apps}
spec: {objects: [{apiVersion: v1, kind: Namespace, metadata: {name: apps}}]}
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.RestartedUnits) != 0 || slices.ContainsFunc(r.Calls, func(c string) bool { return strings.Contains(c, "restart") }) {
		t.Errorf("restarted %v, ran %v", resp.RestartedUnits, r.Calls)
	}
}

// shareLayers are the layer payloads that deliver a share; none for no share.
func shareLayers(t *testing.T, share []byte) []*anypb.Any {
	t.Helper()
	if len(share) == 0 {
		return nil
	}
	p, err := kpki.Payload(share)
	if err != nil {
		t.Fatal(err)
	}
	return []*anypb.Any{p}
}

// factsLayers are the layer facts of an image.json.
func factsLayers(t *testing.T, imageJSON string) []*anypb.Any {
	t.Helper()
	p, err := k8s.FactsPayload([]byte(imageJSON))
	if err != nil {
		t.Fatal(err)
	}
	return []*anypb.Any{p}
}

// A delivery is refused with a payload no layer takes and with two shares.
func TestApplyConfigRefusesPayloads(t *testing.T) {
	worker, _ := kubernetesServer(t, k8s.KindWorker, true)
	other, err := anypb.New(&nodev1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	share := testShare(t, k8s.KindWorker)
	for name, tc := range map[string]struct {
		layers []*anypb.Any
		want   string
	}{
		"another type": {[]*anypb.Any{other}, "which no layer of this image takes"},
		"two shares":   {append(shareLayers(t, share), shareLayers(t, share)...), "two shares"},
	} {
		_, err := deliver(context.Background(), worker, kubernetesIdentity("n1"), &nodev1.ApplyConfigRequest{Layers: tc.layers})
		if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}
