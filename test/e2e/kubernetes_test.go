package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/trevex/chalkos/pkg/lab"
)

// The cluster's VMs on the switch, as nix/testing/cluster.nix configures them.
var kubernetesNodes = []struct {
	name, mac, imageEnv string
	memoryMB            int
}{
	{"cp1", "52:54:00:00:01:11", "CHALKLAB_K8S_CONTROLPLANE_IMAGE_DIR", 2048},
	{"w1", "52:54:00:00:01:12", "CHALKLAB_K8S_WORKER_IMAGE_DIR", 1024},
}

// probeDoneRE is the probe's last fact on the console, written once a boot is up.
var probeDoneRE = regexp.MustCompile(`CHALKTEST done=1`)

// TestKubernetesCluster installs a control plane and a worker that picks its address from a
// subnet, bootstraps the cluster and checks that the worker registers and accepts VXLAN on that
// address only, that pods on both nodes reach each other and resolve the API server's service,
// also after the control plane rebooted with newly issued certificates, and that a worker whose
// link is cut turns NotReady. The images come from a registry the test serves; with
// CHALKLAB_K8S_ONLINE=1 the test does not start that registry, so the nodes' mirror is
// unreachable and containerd falls back to pulling from upstream.
func TestKubernetesCluster(t *testing.T) {
	requireEnv(t, append([]string{"CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS", "CHALKLAB_K8S_CONTROLPLANE_IMAGE_DIR", "CHALKLAB_K8S_WORKER_IMAGE_DIR"}, chalkdEnv...)...)
	online := os.Getenv("CHALKLAB_K8S_ONLINE") == "1"
	if !online {
		requireEnv(t, "CHALKLAB_K8S_IMAGES")
	}
	dir := vmDir(t)
	ctx := context.Background()

	var forwards []lab.GuestForward
	if !online {
		forwards = []lab.GuestForward{{Guest: "10.0.2.100:5000", Host: startRegistry(t)}}
	}
	sw, err := lab.StartSwitch(ctx, filepath.Join(dir, "switch"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sw.Stop)
	apiPort, err := lab.FreePort()
	if err != nil {
		t.Fatal(err)
	}

	nodes := map[string]*node{}
	for _, kn := range kubernetesNodes {
		vmDir := filepath.Join(dir, kn.name)
		if err := os.MkdirAll(vmDir, 0o700); err != nil {
			t.Fatal(err)
		}
		c := lab.VMConfig{
			Dir:           vmDir,
			FirmwareVars:  os.Getenv("CHALKLAB_OVMF_VARS"),
			Disks:         []lab.Disk{prepareDisk(t, vmDir, diskOpts{imageEnv: kn.imageEnv})},
			MemoryMB:      kn.memoryMB,
			GuestForwards: forwards,
			Switch:        sw.Dir,
			MAC:           kn.mac,
		}
		if kn.name == "cp1" {
			c.Forwards = []lab.Forward{{Host: apiPort, Guest: 6443}}
		}
		nodes[kn.name] = startNode(t, c)
	}
	// t.Fatal must not be called from other goroutines than the test's.
	var wg sync.WaitGroup
	errs := make(chan error, len(nodes))
	for name, n := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := install(t, n, name); err != nil {
				errs <- err
				return
			}
			if err := installedNodeAnswers(n, name); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		t.FailNow()
	}

	// chalkd answers while the node's Kubernetes files are still being prepared.
	waitFor(t, 5*time.Minute, "cp1 to wait for bootstrap", func() error {
		out, err := chalkctl(t, nodes["cp1"], "base", "status", "cp1")
		if err != nil {
			return err
		}
		if !strings.Contains(out, "kubernetes controlplane: waiting for bootstrap or for the cluster at https://192.168.100.11:6443") {
			return fmt.Errorf("status before bootstrap:\n%s", out)
		}
		return nil
	})
	if _, err := chalkctl(t, nodes["cp1"], "base", "bootstrap", "cp1"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	kubeconfig := filepath.Join(dir, "kubeconfig")
	if _, err := chalkctl(t, nil, "base", "kubeconfig", "--out", kubeconfig, "--server", fmt.Sprintf("https://127.0.0.1:%d", apiPort)); err != nil {
		t.Fatal(err)
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}

	waitFor(t, 15*time.Minute, "both nodes Ready", func() error { return nodesReady(ctx, cs, "cp1", "w1") })
	waitFor(t, 5*time.Minute, "approved kubelet serving certificates", func() error { return servingCertificates(ctx, cs, "cp1", "w1") })
	if out, err := chalkctl(t, nodes["cp1"], "base", "bootstrap", "cp1"); err == nil || !strings.Contains(out, "bootstrapped already") {
		t.Errorf("a second bootstrap was not refused: %v", err)
	}
	if out, err := chalkctl(t, nodes["w1"], "base", "status", "w1"); err != nil || !strings.Contains(out, "kubernetes worker: joined, node ready: True") {
		t.Errorf("status of w1: %v\n%s", err, out)
	}
	// cp1 has a fixed address; w1 picks the one in its validSubnets.
	for name, want := range map[string]string{"cp1": "192.168.100.11", "w1": "192.168.100.12"} {
		if got := internalIPs(t, ctx, cs, name); !slices.Equal(got, []string{want}) {
			t.Errorf("InternalIP of %s = %v, want %s", name, got, want)
		}
	}
	if rules := vxlanRules(t, nodes["w1"], "w1"); !slices.Equal(rules, []string{"-d 192.168.100.12/32 -p udp -m udp --dport 8472 -m addrtype --dst-type LOCAL --limit-iface-in -j ACCEPT"}) {
		t.Errorf("VXLAN rules of w1 = %q, want one to 192.168.100.12 only", rules)
	}
	anonymousOnlyHealth(t, ctx, cfg)

	createPeers(t, ctx, cs)
	start := time.Now()
	waitFor(t, 10*time.Minute, "pods reaching each other", func() error { return peersReached(ctx, cs, nil) })
	t.Logf("pods reached each other after %v", time.Since(start).Round(time.Second))
	logMemory(t, ctx, cs)

	before := servingCertificate(t, apiPort)
	// The probe's last fact on the console after the reboot marks the new boot; until then the
	// old chalkd may still answer.
	cp1 := nodes["cp1"]
	cp1.vm.Console.Skip()
	if _, err := chalkctl(t, cp1, "base", "reboot", "cp1"); err != nil {
		t.Fatal(err)
	}
	rebootCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if _, err := cp1.vm.Console.WaitFor(rebootCtx, probeDoneRE); err != nil {
		t.Fatalf("cp1 did not boot again: %v", err)
	}
	waitForNode(t, cp1, "cp1")
	waitFor(t, 10*time.Minute, "the API server after the reboot", func() error {
		_, err := cs.Discovery().RESTClient().Get().AbsPath("/readyz").DoRaw(ctx)
		return err
	})
	rebooted := metav1.Now()
	if after := servingCertificate(t, apiPort); after.SerialNumber.Cmp(before.SerialNumber) == 0 {
		t.Error("the API server serves the certificate from before the reboot")
	}
	waitFor(t, 10*time.Minute, "pods reaching each other after the reboot", func() error { return peersReached(ctx, cs, &rebooted) })
	logMemory(t, ctx, cs)

	// A worker whose link is cut turns NotReady, and Ready again once its link is back.
	if err := nodes["w1"].vm.SetLink(false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Minute, "w1 NotReady without its link", func() error {
		if nodesReady(ctx, cs, "w1") == nil {
			return errors.New("w1 is Ready")
		}
		return nil
	})
	if err := nodes["w1"].vm.SetLink(true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Minute, "w1 Ready with its link back", func() error { return nodesReady(ctx, cs, "w1") })
}

// anonymousOnlyHealth checks that a request without credentials reaches the health endpoints
// and nothing else, not even what the default roles allow anonymous users.
func anonymousOnlyHealth(t *testing.T, ctx context.Context, cfg *rest.Config) {
	t.Helper()
	anonymous, err := kubernetes.NewForConfig(rest.AnonymousClientConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}
	client := anonymous.Discovery().RESTClient()
	for _, path := range []string{"/livez", "/readyz", "/healthz"} {
		if _, err := client.Get().AbsPath(path).DoRaw(ctx); err != nil {
			t.Errorf("anonymous %s: %v", path, err)
		}
	}
	if _, err := client.Get().AbsPath("/version").DoRaw(ctx); !apierrors.IsUnauthorized(err) {
		t.Errorf("anonymous /version: %v, want unauthorized", err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, check func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		err := check()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		time.Sleep(5 * time.Second)
	}
}

func nodesReady(ctx context.Context, cs kubernetes.Interface, names ...string) error {
	for _, name := range names {
		n, err := cs.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		ready := false
		for _, c := range n.Status.Conditions {
			ready = ready || c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue
		}
		if !ready {
			return fmt.Errorf("node %s is not ready", name)
		}
	}
	return nil
}

// servingCertificates checks that each node's kubelet got a serving certificate.
func servingCertificates(ctx context.Context, cs kubernetes.Interface, names ...string) error {
	list, err := cs.CertificatesV1().CertificateSigningRequests().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, name := range names {
		issued := false
		for _, csr := range list.Items {
			if csr.Spec.SignerName != certificatesv1.KubeletServingSignerName || csr.Spec.Username != "system:node:"+name {
				continue
			}
			for _, c := range csr.Status.Conditions {
				issued = issued || c.Type == certificatesv1.CertificateApproved && len(csr.Status.Certificate) > 0
			}
		}
		if !issued {
			return fmt.Errorf("no approved serving certificate for %s", name)
		}
	}
	return nil
}

// peerScript serves the pod's name and reports each time it reached the other pod by its DNS
// name after resolving the API server's service.
const peerScript = `mkdir -p /www && hostname > /www/index.html && httpd -p 8080 -h /www
while true; do
  if nslookup kubernetes.default.svc.cluster.local >/dev/null 2>&1 && wget -q -T 2 -O - "http://$PEER.peers.default.svc.cluster.local:8080/" >/dev/null 2>&1; then
    echo reached "$PEER"
  fi
  sleep 2
done`

// createPeers starts pod a on the control plane and pod b on the worker, behind a headless
// service that gives each a DNS name.
func createPeers(t *testing.T, ctx context.Context, cs kubernetes.Interface) {
	t.Helper()
	labels := map[string]string{"app": "peer"}
	_, err := cs.CoreV1().Services("default").Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "peers"},
		Spec:       corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone, Selector: labels, Ports: []corev1.ServicePort{{Port: 8080}}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ name, node, peer string }{{"a", "cp1", "b"}, {"b", "w1", "a"}} {
		_, err := cs.CoreV1().Pods("default").Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: p.name, Labels: labels},
			Spec: corev1.PodSpec{
				Hostname:     p.name,
				Subdomain:    "peers",
				NodeSelector: map[string]string{"kubernetes.io/hostname": p.node},
				Containers: []corev1.Container{{
					Name:    "peer",
					Image:   "docker.io/library/busybox:1.37.0",
					Command: []string{"sh", "-c", peerScript},
					Env:     []corev1.EnvVar{{Name: "PEER", Value: p.peer}},
				}},
			},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// peersReached checks that both pods logged reaching the other, since the time when given.
func peersReached(ctx context.Context, cs kubernetes.Interface, since *metav1.Time) error {
	for _, p := range []struct{ name, peer string }{{"a", "b"}, {"b", "a"}} {
		logs, err := cs.CoreV1().Pods("default").GetLogs(p.name, &corev1.PodLogOptions{SinceTime: since}).DoRaw(ctx)
		if err != nil {
			return fmt.Errorf("logs of pod %s: %w", p.name, err)
		}
		if !strings.Contains(string(logs), "reached "+p.peer) {
			return fmt.Errorf("pod %s has not reached pod %s", p.name, p.peer)
		}
	}
	return nil
}

// logMemory reports the memory each node uses, as its kubelet measures it.
func logMemory(t *testing.T, ctx context.Context, cs kubernetes.Interface) {
	t.Helper()
	for _, kn := range kubernetesNodes {
		logNodeMemory(t, ctx, cs, kn.name, kn.memoryMB)
	}
}

// logNodeMemory reports the memory the node, a VM of memoryMB, uses.
func logNodeMemory(t *testing.T, ctx context.Context, cs kubernetes.Interface, name string, memoryMB int) {
	t.Helper()
	data, err := cs.CoreV1().RESTClient().Get().AbsPath("/api/v1/nodes", name, "proxy/stats/summary").DoRaw(ctx)
	if err != nil {
		t.Logf("memory of %s: %v", name, err)
		return
	}
	var summary struct {
		Node struct {
			Memory struct {
				WorkingSetBytes uint64 `json:"workingSetBytes"`
				AvailableBytes  uint64 `json:"availableBytes"`
			} `json:"memory"`
		} `json:"node"`
	}
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Logf("memory of %s: %v", name, err)
		return
	}
	m := summary.Node.Memory
	t.Logf("memory of %s (%d MiB): %d MiB in use, %d MiB available", name, memoryMB, m.WorkingSetBytes>>20, m.AvailableBytes>>20)
}

// servingCertificate returns the certificate the API server presents.
func servingCertificate(t *testing.T, port int) *x509.Certificate {
	t.Helper()
	conn, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0]
}

// internalIPs returns the InternalIP addresses the node registered.
func internalIPs(t *testing.T, ctx context.Context, cs kubernetes.Interface, name string) []string {
	t.Helper()
	n, err := cs.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var ips []string
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			ips = append(ips, a.Address)
		}
	}
	return ips
}

// vxlanRules returns the rules of the node's VXLAN chain, as the node's preparation logged them
// in this boot.
func vxlanRules(t *testing.T, n *node, name string) []string {
	t.Helper()
	out, err := chalkctl(t, n, "base", "logs", name, "--unit", "chalkos-kubernetes")
	if err != nil {
		t.Fatal(err)
	}
	var rules []string
	for _, line := range strings.Split(out, "\n") {
		if _, rule, ok := strings.Cut(line, "-A chalkos-vxlan "); ok {
			rules = append(rules, strings.TrimSpace(rule))
		}
	}
	return rules
}
