package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"

	"github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes/kubernetestest"
	"github.com/trevex/chalkos/pkg/chalkd/renewal"
	"github.com/trevex/chalkos/pkg/chalkd/renewal/renewaltest"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd/etcdtest"
	"github.com/trevex/chalkos/pkg/kubernetes/manifests"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

func TestBootstrap(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	resp, err := kubernetestest.Bootstrap(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Applied != 19 {
		t.Errorf("applied %d objects", resp.Applied)
	}
	p := s.k.paths
	if bootstrapped, err := knode.Bootstrapped(p); err != nil || !bootstrapped {
		t.Errorf("bootstrapped = %v, %v", bootstrapped, err)
	}
	entries, _ := os.ReadDir(p.Manifests())
	if len(entries) != 4 {
		t.Errorf("static pods %v", entries)
	}
	if pin, err := knode.ReadPin(p); err != nil || len(pin) != 1 || pin[0].String() != "192.168.100.11" {
		t.Errorf("pin = %v, %v, want the node's address", pin, err)
	}
	_, err = kubernetestest.Bootstrap(context.Background(), s)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "bootstrapped already") {
		t.Errorf("second bootstrap: %v", err)
	}
}

func TestBootstrapRefusals(t *testing.T) {
	worker, _ := kubernetesNode(t, k8s.KindWorker, true)
	noShare, _ := kubernetesNode(t, k8s.KindControlPlane, false)
	etcdData, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	write(t, filepath.Join(etcdData.k.paths.EtcdData, "member", "snap", "db"), "")
	noAddress, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	kubernetestest.WithoutAddress(t, noAddress.k.paths, "no node address matches validSubnets 192.168.100.0/24 (the node has 10.0.2.15 on eth0)")
	// A preparation that picked the address and still writes the certificates.
	preparing, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	if err := os.Remove(preparing.k.paths.Prepared()); err != nil {
		t.Fatal(err)
	}
	otherKind, _ := kubernetesNode(t, k8s.KindControlPlane, false)
	kubernetestest.OtherKindsShare(t, otherKind.k.paths)
	noFirewall, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	kubernetestest.WithoutFirewall(t, noFirewall.k.paths)
	for name, tc := range map[string]struct {
		s    *Plugin
		want string
	}{
		"worker":                {worker, "is a worker"},
		"no share":              {noShare, "no Kubernetes share"},
		"etcd with data":        {etcdData, "holds etcd data"},
		"no address":            {noAddress, "no node address matches validSubnets 192.168.100.0/24"},
		"preparing":             {preparing, "the node's Kubernetes files are not prepared yet; see chalkctl logs n1 --unit chalkos-kubernetes"},
		"share of another kind": {otherKind, "preparation failed: the node's share is for a worker node, but its config gives it the type controlplane"},
		"firewall":              {noFirewall, "preparation failed: accept VXLAN to the node's address: exit status 1"},
	} {
		var before map[string]string
		if k := tc.s.k; k != nil {
			before = files(t, k.paths.State, k.paths.Run, k.paths.EtcdData)
		}
		_, err := kubernetestest.Bootstrap(context.Background(), tc.s)
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
		if k := tc.s.k; k != nil {
			after := files(t, k.paths.State, k.paths.Run, k.paths.EtcdData)
			var changed []string
			for path := range maps.Keys(before) {
				if after[path] != before[path] {
					changed = append(changed, path)
				}
			}
			for path := range maps.Keys(after) {
				if _, ok := before[path]; !ok {
					changed = append(changed, path)
				}
			}
			if len(changed) > 0 {
				t.Errorf("%s: the refused bootstrap changed %v", name, changed)
			}
		}
	}
}

// files describes the files under dirs by their size, mode and modification time.
func files(t *testing.T, dirs ...string) map[string]string {
	t.Helper()
	found := map[string]string{}
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			found[path] = fmt.Sprintf("%d %v %v", info.Size(), info.Mode(), info.ModTime())
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
	}
	return found
}

func TestBootstrapReturnsBeforeManifestsApplied(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	s.k.hooks.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) error { <-ctx.Done(); return ctx.Err() }
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := kubernetestest.Bootstrap(ctx, s)
	if connect.CodeOf(err) != connect.CodeDeadlineExceeded {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	// The node stays committed: it never initialises a second cluster.
	if bootstrapped, _ := knode.Bootstrapped(s.k.paths); !bootstrapped {
		t.Error("the node lost its bootstrap marker")
	}
}

// readyServer is an API server whose /readyz answers ok once ready is set.
func readyServer(t *testing.T, ready *atomic.Bool) *rest.Config {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" && ready.Load() {
			io.WriteString(w, "ok")
			return
		}
		http.Error(w, "not ready", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	return &rest.Config{Host: srv.URL, TLSClientConfig: rest.TLSClientConfig{CAData: ca}}
}

func TestEtcdInitialisedAfterReadiness(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	k := s.k
	var ready atomic.Bool
	cfg := readyServer(t, &ready)
	c, err := k8s.ReadCluster(k.paths.Cluster)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := k.applyOnce(ctx, c, cfg); err == nil {
		t.Fatal("applied without a ready API server")
	}
	if initialised, err := knode.EtcdInitialised(k.paths); err != nil || initialised {
		t.Fatalf("before readiness: initialised = %v, %v", initialised, err)
	}

	ready.Store(true)
	// The objects fail, so applying fails after the API server answered ready.
	if _, err := k.applyOnce(context.Background(), c, cfg); err == nil {
		t.Fatal("applied objects that fail")
	}
	if initialised, err := knode.EtcdInitialised(k.paths); err != nil || !initialised {
		t.Errorf("after readiness: initialised = %v, %v", initialised, err)
	}
}

func TestStatusEtcdDataMissing(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	p := s.k.paths
	write(t, p.Bootstrapped(), "")
	write(t, p.EtcdInitialised(), "")
	if got := kubernetestest.Status(t, s).State; got != "etcd data missing: restore etcd or reinstall the node" {
		t.Errorf("state %q", got)
	}
}

// The control plane's loop starts again after an error, with the share as STATE holds it then.
func TestControlPlaneLoopRestarts(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	k := s.k
	k.timing.RestartBackoff = 10 * time.Millisecond
	replaced := kubernetestest.Share(t, k8s.KindControlPlane, "n1")
	want, err := kpki.ParseShare(replaced)
	if err != nil {
		t.Fatal(err)
	}
	var runs atomic.Int32
	var second kpki.Share
	k.hooks.ControlPlane = func(_ context.Context, share kpki.Share, applied func(int)) error {
		if runs.Add(1) == 1 {
			if err := os.WriteFile(k.paths.Share(), replaced, 0o600); err != nil {
				return err
			}
			return errors.New("the API server went away")
		}
		second = share
		applied(19)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	resp, err := kubernetestest.Bootstrap(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Applied != 19 || runs.Load() != 2 {
		t.Errorf("applied %d objects after %d runs", resp.Applied, runs.Load())
	}
	if second.CA.Certificate != want.CA.Certificate {
		t.Error("the restarted loop did not read the share again")
	}
}

// chalkd starts before the preparation at boot; the control plane's loop waits for it.
func TestControlPlaneLoopWaitsForPreparation(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	k := s.k
	p := k.paths
	write(t, p.Bootstrapped(), "")
	write(t, p.Pin(), "192.168.100.11\n")
	if err := os.Remove(p.Prepared()); err != nil {
		t.Fatal(err)
	}
	k.timing.RestartBackoff, k.timing.MaxRestartBackoff = 10*time.Millisecond, 20*time.Millisecond
	runs := make(chan struct{}, 1)
	k.hooks.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) error {
		select {
		case runs <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}
	k.startLoops()
	select {
	case <-runs:
		t.Fatal("the control plane's loop ran before the node's files were prepared")
	case <-time.After(200 * time.Millisecond):
	}
	if err := knode.Prepare(p, time.Now(), kubernetestest.NodeIP("192.168.100.11"), nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runs:
	case <-time.After(time.Minute):
		t.Fatal("the control plane's loop did not start once the node's files were prepared")
	}
}

// A bootstrapped control plane without a pin, as an older image left it, is pinned once its
// files are prepared and its etcd member's peer URL confirms its address.
func TestControlPlaneLoopPins(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	k := s.k
	write(t, k.paths.Bootstrapped(), "")
	share, _ := knode.ReadShare(k.paths)
	k.etcdURL = etcdtest.StartAdvertising(t, *share.EtcdCA, "n1", "https://192.168.100.11:2380").ClientURL
	ran := make(chan struct{})
	k.hooks.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) error {
		close(ran)
		<-ctx.Done()
		return ctx.Err()
	}
	k.startLoops()
	select {
	case <-ran:
	case <-time.After(time.Minute):
		t.Fatal("the control plane's loop did not run")
	}
	if pin, err := knode.ReadPin(k.paths); err != nil || len(pin) != 1 || pin[0].String() != "192.168.100.11" {
		t.Errorf("pin = %v, %v, want the node's address", pin, err)
	}
}

// A bootstrapped control plane whose address is not the one its etcd member was started with is
// never pinned to it.
func TestControlPlaneLoopRefusesMismatchedPin(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	k := s.k
	write(t, k.paths.Bootstrapped(), "")
	share, _ := knode.ReadShare(k.paths)
	k.etcdURL = etcdtest.StartAdvertising(t, *share.EtcdCA, "n1", "https://192.168.100.12:2380").ClientURL
	k.timing.RestartBackoff, k.timing.MaxRestartBackoff = 10*time.Millisecond, 20*time.Millisecond
	var ran atomic.Bool
	k.hooks.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) error {
		ran.Store(true)
		<-ctx.Done()
		return ctx.Err()
	}
	k.startLoops()
	want := "bootstrapped: the node's address differs from its etcd peer URL https://192.168.100.12:2380; restore the address"
	eventually(t, "the mismatch's state", func() bool {
		st, err := k.status(context.Background())
		return err == nil && st.State == want
	})
	if exists(k.paths.Pin()) {
		t.Error("the node was pinned to an address its etcd member does not have")
	}
	if ran.Load() {
		t.Error("the control plane's loop ran without the node's pin")
	}
}

func TestCredentialIsShortLived(t *testing.T) {
	share, err := kpki.ParseShare(kubernetestest.Share(t, k8s.KindControlPlane, "n1"))
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	c := newCredential(share, time.Hour, func() time.Time { return clock })
	first, err := c.GetClientCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := first.Leaf.NotAfter.Sub(clock); d < 59*time.Minute || d > time.Hour {
		t.Errorf("the certificate expires after %v, want an hour", d)
	}
	if !slices.Equal(first.Leaf.Subject.Organization, []string{kpki.MastersGroup}) {
		t.Errorf("groups %v", first.Leaf.Subject.Organization)
	}

	clock = clock.Add(29 * time.Minute)
	if again, err := c.GetClientCertificate(nil); err != nil || again != first {
		t.Errorf("issued again before half the lifetime passed: %v", err)
	}
	clock = clock.Add(2 * time.Minute)
	next, err := c.GetClientCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if next == first || !next.Leaf.NotAfter.After(first.Leaf.NotAfter) {
		t.Error("not issued again after half the lifetime passed")
	}
	if !c.rotated() || c.rotated() {
		t.Error("the replaced certificate's connections are not closed exactly once")
	}
}

// Once chalkd stops, a startLoops or reloadControlPlane that arrives meanwhile, as from
// ApplyConfig, starts no loop again.
func TestStartAfterStopDoesNothing(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	k := s.k
	p := k.paths
	var runs atomic.Int32
	k.hooks.ControlPlane = func(ctx context.Context, _ kpki.Share, _ func(int)) error {
		runs.Add(1)
		<-ctx.Done()
		return ctx.Err()
	}
	k.stop()
	// Not bootstrapped, the node would join.
	k.startLoops()
	k.reloadControlPlane()
	write(t, p.Bootstrapped(), "")
	write(t, p.Pin(), "192.168.100.11\n")
	k.startLoops()
	k.reloadControlPlane()
	time.Sleep(200 * time.Millisecond)
	k.mu.Lock()
	started, joining := k.started, k.joining
	k.mu.Unlock()
	if started || joining || runs.Load() != 0 {
		t.Errorf("after Stop: started %v, joining %v, %d runs of the control plane's loop", started, joining, runs.Load())
	}
}

func TestControlPlaneRenewsItsCertificates(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	if _, err := kubernetestest.Bootstrap(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	k := s.k
	k.mu.Lock()
	leaves := k.leaves
	k.mu.Unlock()
	if leaves == nil {
		t.Fatal("a bootstrapped control plane does not renew its certificates")
	}
	due, expires, err := leaves.Due(time.Now())
	if err != nil || due {
		t.Fatalf("due %v, %v with new certificates", due, err)
	}
	// Due once a third of a year remains.
	if due, _, _ := leaves.Due(expires.Add(-130 * 24 * time.Hour)); due {
		t.Error("due with 130 days left")
	}
	if due, _, _ := leaves.Due(expires.Add(-100 * 24 * time.Hour)); !due {
		t.Error("not due with 100 days left")
	}
	apiServer := filepath.Join(k.paths.PKI, kpki.FileAPIServer)
	before, _ := os.ReadFile(apiServer)
	if err := leaves.Renew(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(apiServer); bytes.Equal(after, before) {
		t.Error("the API server's certificate was not issued again")
	}
	if k.leavesProblem() != "" {
		t.Errorf("problem = %q", k.leavesProblem())
	}
}

// staleStaticPod makes the API server's static pod name other certificates than the current
// ones, as a renewal interrupted before it rendered the pods leaves it, and returns the manifest.
func staleStaticPod(t *testing.T, p knode.Paths) string {
	t.Helper()
	path := filepath.Join(p.Manifests(), "kube-apiserver.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := json.Unmarshal(data, &pod); err != nil {
		t.Fatal(err)
	}
	pod.Annotations[manifests.CertificatesAnnotation] = "the certificates before"
	stale, err := json.MarshalIndent(&pod, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(stale))
	return string(data)
}

func TestControlPlaneRendersStaleStaticPods(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	if _, err := kubernetestest.Bootstrap(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	k := s.k
	p := k.paths
	k.mu.Lock()
	leaves := k.leaves
	k.mu.Unlock()
	apiServer := filepath.Join(p.PKI, kpki.FileAPIServer)
	before, _ := os.ReadFile(apiServer)

	// The renewal is due at once and renders the pods, without issuing certificates again.
	rendered := staleStaticPod(t, p)
	if due, _, err := leaves.Due(time.Now()); err != nil || !due {
		t.Errorf("due %v, %v with stale static pods, want due", due, err)
	}
	if err := leaves.Renew(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(p.Manifests(), "kube-apiserver.json")); string(got) != rendered {
		t.Error("the renewal did not render the stale static pod")
	}
	if after, _ := os.ReadFile(apiServer); !bytes.Equal(after, before) {
		t.Error("the renewal issued certificates again while the pods ran on the previous ones")
	}

	// The control plane's loop renders them as it starts.
	k.stopLoops()
	staleStaticPod(t, p)
	k.startLoops()
	eventually(t, "the stale static pod to be rendered", func() bool {
		data, _ := os.ReadFile(filepath.Join(p.Manifests(), "kube-apiserver.json"))
		return string(data) == rendered
	})
}

// Stopping the loops, as before the node leaves etcd, waits for a renewal of the control plane's
// certificates that runs, which would render static pods.
func TestStopLoopsWaitsForTheCertificateRenewal(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	k := s.k
	entered, release := make(chan struct{}), make(chan struct{})
	k.leaves = renewal.New("test", func(time.Time) (bool, time.Time, error) {
		return true, time.Time{}, nil
	}, func(context.Context, time.Time) error {
		close(entered)
		<-release
		return nil
	})
	write(t, k.paths.Bootstrapped(), "")
	write(t, k.paths.Pin(), "192.168.100.11\n")
	k.startLoops()
	k.leaves.Force()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the renewal did not run")
	}
	stopped := make(chan struct{})
	go func() { k.stopLoops(); close(stopped) }()
	select {
	case <-stopped:
		t.Error("the loops stopped while a renewal still ran")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Error("the loops did not stop once the renewal ended")
	}
}

// TestObjectsAreTheAddOnsThenTheAddonDocuments: chalkd applies the add-ons it renders for the
// cluster, then the objects of the stored Addon documents.
func TestObjectsAreTheAddOnsThenTheAddonDocuments(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, false)
	k := newSide(Options{Hooks: Hooks{Slot: s.node.Slot}})
	c, err := k8s.ReadCluster(s.k.paths.Cluster)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := config.Parse(strings.NewReader(`apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Addon
metadata: {name: apps}
spec: {objects: [{apiVersion: v1, kind: Namespace, metadata: {name: apps}}]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := slot.Write(slotOf(t, s), slot.Stored{Delivered: docs, Documents: docs, Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	objects, err := k.hooks.Objects(c)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(objects); n < 2 || objects[0]["kind"] != "ClusterRoleBinding" || objects[n-1]["kind"] != "Namespace" {
		t.Errorf("objects = %v", objects)
	}
}

// Objects that fail to apply, such as a bad Addon document's, leave the approver of kubelet
// serving certificates running while the apply retries.
func TestApproverRunsWhileObjectsFail(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	k := s.k
	var ready atomic.Bool
	ready.Store(true)
	cfg := readyServer(t, &ready)
	c, err := k8s.ReadCluster(k.paths.Cluster)
	if err != nil {
		t.Fatal(err)
	}
	approving := make(chan struct{})
	k.hooks.Approve = func(ctx context.Context, _ *rest.Config) {
		close(approving)
		<-ctx.Done()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() {
		served <- k.serveControlPlane(ctx, c, cfg, func(int) { t.Error("applied objects that fail") })
	}()
	select {
	case <-approving:
	case <-time.After(time.Minute):
		t.Fatal("the approver did not start")
	}
	cancel()
	if err := <-served; !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

// The add-ons are applied whether or not the revoked nodes' admission policy can be: the
// approver applies the policy and reports its failures apart, so an API server that refuses the
// policy, here by answering nothing but its readiness, still gets the add-ons.
func TestApplyOnceLeavesTheRevokedNodesToTheApprover(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	k := s.k
	var ready atomic.Bool
	ready.Store(true)
	cfg := readyServer(t, &ready)
	c, err := k8s.ReadCluster(k.paths.Cluster)
	if err != nil {
		t.Fatal(err)
	}
	k.hooks.Objects = func(k8s.Cluster) ([]map[string]any, error) { return nil, nil }
	if n, err := k.applyOnce(context.Background(), c, cfg); err != nil || n != 0 {
		t.Errorf("applyOnce = %d, %v", n, err)
	}
}

// After a reboot chalkd's loops start before the preparation wrote the control plane's
// certificates: the renewal waits for them and issues none of its own.
func TestControlPlaneRenewalWaitsForThePreparation(t *testing.T) {
	s, _ := kubernetesNode(t, k8s.KindControlPlane, true)
	p := s.k.paths
	// RUN is empty after a reboot.
	if err := os.RemoveAll(p.Run); err != nil {
		t.Fatal(err)
	}
	r := s.k.controlPlaneRenewal()
	renew := r.Renew
	renewals := 0
	r.Renew = func(ctx context.Context, now time.Time) error {
		renewals++
		return renew(ctx, now)
	}
	s.k.leaves = r
	start := time.Now()
	clock := renewaltest.NewClock(start)
	r.Now = clock.Now
	r.After = clock.After
	advance := renewaltest.Run(t, r.Run, clock)
	now := start
	for range 3 {
		now = now.Add(advance(now))
		if r.Problem() != "" {
			t.Errorf("problem before the preparation = %q", r.Problem())
		}
		certificates, _ := s.Certificates(context.Background(), time.Now())
		for _, c := range certificates {
			if strings.Contains(c.Problem, "renewal failing") {
				t.Errorf("status %s: %s", c.Name, c.Problem)
			}
		}
	}
	if exists(p.PKI) {
		t.Error("the renewal wrote the control plane's certificates before the preparation")
	}
	// The certificates the preparation writes are new: none are renewed, so no static pod
	// starts again.
	if err := knode.Prepare(p, now, kubernetestest.NodeIP("192.168.100.11"), nil); err != nil {
		t.Fatal(err)
	}
	apiServer := filepath.Join(p.PKI, kpki.FileAPIServer)
	prepared, err := os.ReadFile(apiServer)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		now = now.Add(advance(now))
	}
	if renewals != 0 {
		t.Errorf("renewed %d times after the preparation", renewals)
	}
	if after, _ := os.ReadFile(apiServer); string(after) != string(prepared) {
		t.Error("the certificates the preparation wrote were issued again")
	}
	if r.Problem() != "" {
		t.Errorf("problem after the preparation = %q", r.Problem())
	}
}
