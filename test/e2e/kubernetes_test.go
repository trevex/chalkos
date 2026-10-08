package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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

// TestKubernetesCluster installs a dual-stack cluster, IPv4 primary, of a control plane and a
// worker that picks its addresses from subnets, bootstraps it and checks that the worker
// registers and accepts VXLAN on those addresses only, that pods on both nodes have addresses of
// both families and reach each other over both, directly, through a dual-stack service and at a
// host port, and resolve the API server's service and other names through the IPv4 DNS address,
// all of it again after the control plane rebooted with newly issued certificates, and that a
// worker whose link is cut turns NotReady.
// The images come from a registry the test serves; with CHALKLAB_K8S_ONLINE=1 the test does not
// start that registry, so the nodes' mirror is unreachable and containerd falls back to pulling
// from upstream.
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
	// The checks of the nodes and of the cluster's networking, again after cp1's reboot. cp1 has
	// fixed addresses; w1 picks the ones in its validSubnets.
	checkNetworking := func() {
		for _, name := range []string{"cp1", "w1"} {
			dualStackNode(t, ctx, cs, nodes[name], name)
		}
		anonymousOnlyHealth(t, ctx, cfg)
		kubeProxyNFTables(t, ctx, cs)
		dnsService(t, ctx, cs)
	}
	checkNetworking()

	p := peers{
		nodes:        [2]string{"cp1", "w1"},
		nodeIPs:      [2][]string{dualStackAddresses["cp1"], dualStackAddresses["w1"]},
		families:     []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol},
		dnsIP:        "10.96.0.10",
		kubernetesIP: "10.96.0.1",
	}
	p.create(t, ctx, cs)
	start := time.Now()
	waitFor(t, 10*time.Minute, "pods reaching each other", func() error { return p.reached(ctx, cs, nil) })
	t.Logf("pods reached each other after %v", time.Since(start).Round(time.Second))
	p.check(t, ctx, cs)
	logMemory(t, ctx, cs)

	before := servingCertificate(t, apiPort)
	// The probe's last fact on the console after the reboot marks the new boot; until then the
	// old chalkd may still answer.
	cp1 := nodes["cp1"]
	cp1.vm.Console.Skip()
	requested := time.Now()
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
	waitFor(t, 10*time.Minute, "pods reaching each other after the reboot", func() error { return p.reached(ctx, cs, &rebooted) })
	// flannel announces its addresses when it starts, so the annotations are this boot's once it
	// started again.
	waitFor(t, 5*time.Minute, "flannel on cp1 to start after the reboot", func() error { return flannelStarted(ctx, cs, "cp1", requested) })
	checkNetworking()
	p.check(t, ctx, cs)
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

// peerHostPort is the port of the peers on their nodes' addresses.
const peerHostPort = 31080

// peerScript serves the pod's name, says which name server it uses, and reports each time it
// reached the other pod in a family: at the addresses its name under the headless service
// resolves to, at those of the other pod's service, for each record type of TYPES, and at its
// host port on each of the addresses in PEER_NODE. It also reports the addresses the API
// server's service resolves to in the primary family, the first of TYPES.
const peerScript = `mkdir -p /www && hostname > /www/index.html && httpd -p 8080 -h /www
echo "name server $(awk '/^nameserver/ {print $2; exit}' /etc/resolv.conf)"
reach() {
  case $1 in *:*) url="http://[$1]:$2/" ;; *) url="http://$1:$2/" ;; esac
  wget -q -T 2 -O - "$url" 2>/dev/null | grep -qx "$PEER"
}
while true; do
  for ip in $(nslookup -type=${TYPES%% *} kubernetes.default.svc.cluster.local 2>/dev/null | awk '/^Address: / {print $2}'); do
    echo "resolved kubernetes $ip"
  done
  for name in "$PEER.peers" "peer-$PEER"; do
    for type in $TYPES; do
      for ip in $(nslookup -type=$type "$name.default.svc.cluster.local" 2>/dev/null | awk '/^Address: / {print $2}'); do
        if reach "$ip" 8080; then
          echo "reached $name $type $ip"
        fi
      done
    done
  done
  for ip in $PEER_NODE; do
    if reach "$ip" $HOST_PORT; then
      echo "reached host port $ip"
    fi
  done
  sleep 2
done`

// peers are pod a on one node and pod b on another, which reach each other in every family of
// the cluster: at the addresses their names under a headless service resolve to, through a
// service of each, and at a host port on the other's node.
type peers struct {
	nodes [2]string
	// nodeIPs are the addresses of the nodes, the primary family's first.
	nodeIPs [2][]string
	// families are the cluster's, the primary one first.
	families []corev1.IPFamily
	// dnsIP is the cluster DNS's address, which the pods resolve names through.
	dnsIP string
	// kubernetesIP is the API server's service address, the first of the primary family's
	// service range.
	kubernetesIP string
}

// recordTypes are the DNS record types of the families, as nslookup names them.
func (p peers) recordTypes() []string {
	var types []string
	for _, f := range p.families {
		types = append(types, map[corev1.IPFamily]string{corev1.IPv4Protocol: "a", corev1.IPv6Protocol: "aaaa"}[f])
	}
	return types
}

// create starts the pods behind a headless service that gives each a DNS name, and gives each a
// service of its own; all of them have the cluster's families. A headless service has the primary
// family alone unless it asks for more.
func (p peers) create(t *testing.T, ctx context.Context, cs kubernetes.Interface) {
	t.Helper()
	policy := corev1.IPFamilyPolicySingleStack
	if len(p.families) == 2 {
		policy = corev1.IPFamilyPolicyRequireDualStack
	}
	_, err := cs.CoreV1().Services("default").Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "peers"},
		Spec: corev1.ServiceSpec{
			ClusterIP:      corev1.ClusterIPNone,
			IPFamilyPolicy: &policy,
			IPFamilies:     p.families,
			Selector:       map[string]string{"app": "peer"},
			Ports:          []corev1.ServicePort{{Port: 8080}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		svc, err := cs.CoreV1().Services("default").Create(ctx, &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "peer-" + name},
			Spec: corev1.ServiceSpec{
				IPFamilyPolicy: &policy,
				IPFamilies:     p.families,
				Selector:       map[string]string{"peer": name},
				Ports:          []corev1.ServicePort{{Port: 8080}},
			},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(svc.Spec.IPFamilies, p.families) || len(svc.Spec.ClusterIPs) != len(p.families) {
			t.Errorf("service peer-%s: families %v, addresses %v, want one of each of %v", name, svc.Spec.IPFamilies, svc.Spec.ClusterIPs, p.families)
		}
	}
	for i, pod := range []struct{ name, peer string }{{"a", "b"}, {"b", "a"}} {
		_, err := cs.CoreV1().Pods("default").Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: pod.name, Labels: map[string]string{"app": "peer", "peer": pod.name}},
			Spec: corev1.PodSpec{
				Hostname:     pod.name,
				Subdomain:    "peers",
				NodeSelector: map[string]string{"kubernetes.io/hostname": p.nodes[i]},
				Containers: []corev1.Container{{
					Name:    "peer",
					Image:   "docker.io/library/busybox:1.37.0",
					Command: []string{"sh", "-c", peerScript},
					Ports:   []corev1.ContainerPort{{ContainerPort: 8080, HostPort: peerHostPort}},
					Env: []corev1.EnvVar{
						{Name: "PEER", Value: pod.peer},
						{Name: "TYPES", Value: strings.Join(p.recordTypes(), " ")},
						{Name: "PEER_NODE", Value: strings.Join(p.nodeIPs[1-i], " ")},
						{Name: "HOST_PORT", Value: strconv.Itoa(peerHostPort)},
					},
				}},
			},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// reached checks that both pods logged resolving the API server's service and reaching the other
// in every family, directly, through its service and at its host port, since the time when
// given.
func (p peers) reached(ctx context.Context, cs kubernetes.Interface, since *metav1.Time) error {
	for i, pod := range []struct{ name, peer string }{{"a", "b"}, {"b", "a"}} {
		logs, err := cs.CoreV1().Pods("default").GetLogs(pod.name, &corev1.PodLogOptions{SinceTime: since}).DoRaw(ctx)
		if err != nil {
			return fmt.Errorf("logs of pod %s: %w", pod.name, err)
		}
		if !strings.Contains(string(logs), "resolved kubernetes "+p.kubernetesIP+"\n") {
			return fmt.Errorf("pod %s has not resolved kubernetes.default.svc.cluster.local to %s", pod.name, p.kubernetesIP)
		}
		for _, name := range []string{pod.peer + ".peers", "peer-" + pod.peer} {
			for _, typ := range p.recordTypes() {
				if via := name + " " + typ + " "; !strings.Contains(string(logs), "reached "+via) {
					return fmt.Errorf("pod %s has not reached %s", pod.name, via)
				}
			}
		}
		for _, ip := range p.nodeIPs[1-i] {
			if !strings.Contains(string(logs), "reached host port "+ip+"\n") {
				return fmt.Errorf("pod %s has not reached pod %s's host port at %s", pod.name, pod.peer, ip)
			}
		}
	}
	return nil
}

// check checks that the pods have an address of each of the cluster's families, the primary
// one's first, and resolve names through the cluster DNS's address.
func (p peers) check(t *testing.T, ctx context.Context, cs kubernetes.Interface) {
	t.Helper()
	for _, name := range []string{"a", "b"} {
		pod, err := cs.CoreV1().Pods("default").Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var families []corev1.IPFamily
		for _, ip := range pod.Status.PodIPs {
			family := corev1.IPv4Protocol
			if strings.Contains(ip.IP, ":") {
				family = corev1.IPv6Protocol
			}
			families = append(families, family)
		}
		if !slices.Equal(families, p.families) {
			t.Errorf("addresses of pod %s %v, want one of each of %v", name, pod.Status.PodIPs, p.families)
		}
		logs, err := cs.CoreV1().Pods("default").GetLogs(name, &corev1.PodLogOptions{}).DoRaw(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(logs), "name server "+p.dnsIP+"\n") {
			t.Errorf("pod %s does not resolve through %s:\n%s", name, p.dnsIP, logs)
		}
	}
}

// dualStackAddresses are the addresses of the nodes of the dual-stack cluster, IPv4 first. w1's
// are on a dummy interface, routed through its address on the cluster network.
var dualStackAddresses = map[string][]string{"cp1": {"192.168.100.11", "fd00:100::11"}, "w1": {"192.168.200.12", "fd00:200::12"}}

// dualStackNode checks that the node registered its addresses, one per family, IPv4 first; that
// flannel uses the same, and the VXLAN rules of this boot take VXLAN to them alone, from the
// cluster's source ranges, on the interface holding the address or, on w1's dummy interface, on
// any but the pod network's, and drop the VXLAN pods send to those ranges; and that the node has
// a pod range of each family.
func dualStackNode(t *testing.T, ctx context.Context, cs kubernetes.Interface, n *node, name string) {
	t.Helper()
	want := dualStackAddresses[name]
	if got := internalIPs(t, ctx, cs, name); !slices.Equal(got, want) {
		t.Errorf("InternalIPs of %s = %v, want %v", name, got, want)
	}
	waitFor(t, 5*time.Minute, "flannel on "+name+" to use the node's addresses", func() error { return flannelAddresses(ctx, cs, name, want) })
	if err := dualStackPodRanges(ctx, cs, name); err != nil {
		t.Error(err)
	}
	rules, drops := vxlanRules(t, n, "base", name)
	slices.Sort(rules)
	arrival := " fib daddr . iif type local"
	if name == "w1" {
		arrival = ` iifname != { "cni*", "flannel*", "kube-*", "veth*" }`
	}
	if want := []string{
		"ip saddr { 192.168.100.0/24, 192.168.200.0/24 } ip daddr " + want[0] + " udp dport 8472" + arrival + " meta mark set meta mark | 0x01000000",
		"ip6 saddr { fd00:100::/64, fd00:200::/64 } ip6 daddr " + want[1] + " udp dport 8472" + arrival + " meta mark set meta mark | 0x01000000",
	}; !slices.Equal(rules, want) {
		t.Errorf("VXLAN rules of %s = %q, want %q", name, rules, want)
	}
	slices.Sort(drops)
	if want := []string{
		"ip saddr 10.244.0.0/16 ip daddr { 192.168.100.0/24, 192.168.200.0/24 } udp dport 8472 drop",
		"ip6 saddr fd00:10:244::/56 ip6 daddr { fd00:100::/64, fd00:200::/64 } udp dport 8472 drop",
	}; !slices.Equal(drops, want) {
		t.Errorf("VXLAN rules of %s for pods = %q, want %q", name, drops, want)
	}
}

// flannelAddresses checks that flannel on the node announced exactly the addresses, one per
// family.
func flannelAddresses(ctx context.Context, cs kubernetes.Interface, name string, want []string) error {
	n, err := cs.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	got := []string{n.Annotations["flannel.alpha.coreos.com/public-ip"], n.Annotations["flannel.alpha.coreos.com/public-ipv6"]}
	if !slices.Equal(got, want) {
		return fmt.Errorf("flannel on %s uses %v, want %v", name, got, want)
	}
	return nil
}

// dualStackPodRanges checks that the node has a pod range of each family, IPv4 first, each a
// node-sized part of the cluster's: a /24 of 10.244.0.0/16 and a /64 of fd00:10:244::/56.
func dualStackPodRanges(ctx context.Context, cs kubernetes.Interface, name string) error {
	n, err := cs.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	cluster := []netip.Prefix{netip.MustParsePrefix("10.244.0.0/16"), netip.MustParsePrefix("fd00:10:244::/56")}
	sizes := []int{24, 64}
	r := n.Spec.PodCIDRs
	ok := len(r) == len(cluster)
	for i := 0; ok && i < len(r); i++ {
		p, err := netip.ParsePrefix(r[i])
		ok = err == nil && p.Bits() == sizes[i] && cluster[i].Contains(p.Addr())
	}
	if !ok {
		return fmt.Errorf("pod ranges of %s %v, want a /24 of 10.244.0.0/16 and a /64 of fd00:10:244::/56, IPv4 first", name, r)
	}
	return nil
}

// flannelStarted checks that flannel's container on the node runs, started after since.
func flannelStarted(ctx context.Context, cs kubernetes.Interface, name string, since time.Time) error {
	pods, err := cs.CoreV1().Pods("kube-flannel").List(ctx, metav1.ListOptions{LabelSelector: "app=flannel", FieldSelector: "spec.nodeName=" + name})
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		for _, s := range pod.Status.ContainerStatuses {
			if s.Name == "kube-flannel" && s.State.Running != nil && s.State.Running.StartedAt.After(since) {
				return nil
			}
		}
	}
	return fmt.Errorf("flannel on %s has not started since %v", name, since.Format(time.RFC3339))
}

// kubeProxyNFTables checks that kube-proxy runs in nftables mode on every node.
func kubeProxyNFTables(t *testing.T, ctx context.Context, cs kubernetes.Interface) {
	t.Helper()
	var pods *corev1.PodList
	waitFor(t, 2*time.Minute, "a kube-proxy pod on each node", func() error {
		var err error
		if pods, err = cs.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{LabelSelector: "k8s-app=kube-proxy"}); err != nil {
			return err
		}
		if len(pods.Items) != len(kubernetesNodes) {
			return fmt.Errorf("%d kube-proxy pods, want %d", len(pods.Items), len(kubernetesNodes))
		}
		return nil
	})
	for _, pod := range pods.Items {
		waitFor(t, 2*time.Minute, "kube-proxy's logs on "+pod.Spec.NodeName, func() error {
			logs, err := cs.CoreV1().Pods("kube-system").GetLogs(pod.Name, &corev1.PodLogOptions{}).DoRaw(ctx)
			if err != nil {
				return err
			}
			if !strings.Contains(string(logs), "Using nftables Proxier") {
				return fmt.Errorf("kube-proxy on %s does not use nftables:\n%s", pod.Spec.NodeName, logs)
			}
			return nil
		})
	}
}

// dnsService checks that the cluster DNS has the IPv4 DNS address alone, as kubeadm's has.
func dnsService(t *testing.T, ctx context.Context, cs kubernetes.Interface) {
	t.Helper()
	svc, err := cs.CoreV1().Services("kube-system").Get(ctx, "kube-dns", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(svc.Spec.ClusterIPs, []string{"10.96.0.10"}) || !slices.Equal(svc.Spec.IPFamilies, []corev1.IPFamily{corev1.IPv4Protocol}) {
		t.Errorf("kube-dns: addresses %v, families %v, want 10.96.0.10 alone", svc.Spec.ClusterIPs, svc.Spec.IPFamilies)
	}
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

// vxlanRuleRE is a rule of the node's VXLAN table that marks VXLAN to the node, as nft lists it;
// vxlanDropRE one that drops VXLAN pods send through the node.
var (
	vxlanRuleRE = regexp.MustCompile(`ip6? (saddr \{[^}]*\} ip6? )?daddr \S+ udp dport 8472 .*meta mark set .*$`)
	vxlanDropRE = regexp.MustCompile(`ip6? saddr \S+ ip6? daddr .* udp dport 8472 drop$`)
)

// vxlanRules returns the marking and the dropping rules of the node's VXLAN table, as the node's
// preparation logged them last in this boot; manifest names the cluster's manifest, base or ha.
func vxlanRules(t *testing.T, n *node, manifest, name string) (marks, drops []string) {
	t.Helper()
	out, err := chalkctl(t, n, manifest, "logs", name, "--unit", "chalkos-kubernetes")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out, "\n") {
		// The preparation empties the table first and lists it each time.
		if strings.Contains(line, "table inet chalkos-vxlan {") {
			marks, drops = nil, nil
		}
		if rule := vxlanRuleRE.FindString(line); rule != "" {
			marks = append(marks, strings.TrimSpace(rule))
		}
		if rule := vxlanDropRE.FindString(line); rule != "" {
			drops = append(drops, strings.TrimSpace(rule))
		}
	}
	return marks, drops
}
