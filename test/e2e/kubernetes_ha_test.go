package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/trevex/chalkos/pkg/lab"
)

// The HA cluster's control planes on the switch, as nix/testing/cluster.nix configures them.
var haNodes = []struct{ name, mac string }{
	{"cp1", "52:54:00:00:02:11"},
	{"cp2", "52:54:00:00:02:12"},
	{"cp3", "52:54:00:00:02:13"},
}

// haMemoryMB is each control plane's memory: three of them fit the local budget of 4.5 GiB.
const haMemoryMB = 1536

// haCluster is the VMs of the HA test.
type haCluster struct {
	t        *testing.T
	dir      string
	sw       *lab.Switch
	forwards []lab.GuestForward
	nodes    map[string]*haNode
	// boots counts each node's VMs, which get directories of their own.
	boots map[string]int
}

// haNode is a control plane's VM; its API server is reachable on apiPort.
type haNode struct {
	*node
	apiPort int
	dir     string
}

// start boots the node's VM with the MAC on the cluster network. With fresh it gets a new
// directory, so a blank disk and TPM; otherwise it boots the disk and TPM it had.
func (c *haCluster) start(name, mac string, fresh bool) *haNode {
	c.t.Helper()
	old := c.nodes[name]
	dir := ""
	if old != nil && !fresh {
		dir = old.dir
	} else {
		c.boots[name]++
		dir = filepath.Join(c.dir, fmt.Sprintf("%s-%d", name, c.boots[name]))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			c.t.Fatal(err)
		}
	}
	var disks []lab.Disk
	if old == nil || fresh {
		disks = []lab.Disk{prepareDisk(c.t, dir, diskOpts{imageEnv: "CHALKLAB_K8S_HA_IMAGE_DIR"})}
	} else {
		disks = old.vm.Config.Disks
	}
	apiPort, err := lab.FreePort()
	if err != nil {
		c.t.Fatal(err)
	}
	n := startNode(c.t, lab.VMConfig{
		Dir:           dir,
		FirmwareVars:  os.Getenv("CHALKLAB_OVMF_VARS"),
		Disks:         disks,
		MemoryMB:      haMemoryMB,
		GuestForwards: c.forwards,
		Switch:        c.sw.Dir,
		MAC:           mac,
		Forwards:      []lab.Forward{{Host: apiPort, Guest: 6443}},
	})
	hn := &haNode{node: n, apiPort: apiPort, dir: dir}
	c.nodes[name] = hn
	return hn
}

// stop powers the node's VM off; with discard its directory goes too.
func (c *haCluster) stop(name string, discard bool) {
	c.t.Helper()
	n := c.nodes[name]
	n.vm.Stop()
	if discard {
		if err := os.RemoveAll(n.dir); err != nil {
			c.t.Fatal(err)
		}
	}
}

// reinstall gives the node a blank disk and TPM and installs it again.
func (c *haCluster) reinstall(name, mac string) {
	c.t.Helper()
	c.stop(name, true)
	n := c.start(name, mac, true)
	if err := installFrom(c.t, n.node, name, "ha"); err != nil {
		c.t.Fatal(err)
	}
	waitForNode(c.t, n.node, name)
}

// chalkctl runs chalkctl with the HA cluster's manifest against the node.
func (c *haCluster) chalkctl(name string, args ...string) (string, error) {
	return chalkctl(c.t, c.nodes[name].node, "ha", args...)
}

// kubernetes returns the line of the node's status about Kubernetes.
func (c *haCluster) kubernetes(name string) string {
	out, err := c.chalkctl(name, "status", name)
	if err != nil {
		return fmt.Sprintf("status failed: %v", err)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "kubernetes ") {
			return line
		}
	}
	return ""
}

// holders returns the nodes whose status says they hold the VIP.
func (c *haCluster) holders(names ...string) []string {
	var holders []string
	for _, name := range names {
		if strings.HasSuffix(c.kubernetes(name), "vip holder") {
			holders = append(holders, name)
		}
	}
	return holders
}

// members checks that etcd, as via's member sees it, has exactly the voters named, all healthy.
func (c *haCluster) members(via string, voters ...string) error {
	out, err := c.chalkctl(via, "etcd", "members", "--via", via)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var healthy []string
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) >= 5 && fields[3] == "voter" && fields[4] == "healthy" {
			healthy = append(healthy, fields[0])
		}
	}
	slices.Sort(healthy)
	if len(lines)-1 != len(voters) || !slices.Equal(healthy, voters) {
		return fmt.Errorf("etcd members:\n%s\nwant the healthy voters %v", out, voters)
	}
	return nil
}

// client reaches the API server on the node.
func (c *haCluster) client(name string) kubernetes.Interface {
	c.t.Helper()
	kubeconfig := filepath.Join(c.dir, "kubeconfig-"+name)
	os.Remove(kubeconfig)
	if _, err := chalkctl(c.t, nil, "ha", "kubeconfig", "--out", kubeconfig, "--server", fmt.Sprintf("https://127.0.0.1:%d", c.nodes[name].apiPort)); err != nil {
		c.t.Fatal(err)
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		c.t.Fatal(err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		c.t.Fatal(err)
	}
	return cs
}

// TestKubernetesHA runs three control planes of an IPv6-only cluster behind the VIP
// fd00:100::10. cp1 is bootstrapped and holds the VIP; cp2 and cp3 join etcd on their own. Nodes,
// etcd members and pods have IPv6 addresses alone. The VIP moves when its holder's link is cut
// and stays with one holder once the link is back. A pinned node without its address runs
// nothing while the cluster stays healthy. A node that left etcd and was reinstalled joins again;
// one reinstalled without leaving refuses to bootstrap and finds its stale member until an
// operator removes it.
func TestKubernetesHA(t *testing.T) {
	requireEnv(t, append([]string{"CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS", "CHALKLAB_K8S_HA_IMAGE_DIR", "CHALKLAB_K8S_IMAGES"}, chalkdEnv...)...)
	ctx := context.Background()
	start := time.Now()
	c := &haCluster{t: t, dir: vmDir(t), nodes: map[string]*haNode{}, boots: map[string]int{}}
	c.forwards = []lab.GuestForward{{Guest: "10.0.2.100:5000", Host: startRegistry(t)}}
	sw, err := lab.StartSwitch(ctx, filepath.Join(c.dir, "switch"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sw.Stop)
	c.sw = sw
	for _, hn := range haNodes {
		c.start(hn.name, hn.mac, false)
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(haNodes))
	for _, hn := range haNodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := c.nodes[hn.name].node
			if err := installFrom(t, n, hn.name, "ha"); err != nil {
				errs <- err
				return
			}
			if err := installedNodeAnswers(n, hn.name); err != nil {
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
	t.Logf("installed after %v", time.Since(start).Round(time.Second))

	waitFor(t, 5*time.Minute, "cp1 to wait for bootstrap or the cluster", func() error {
		if got := c.kubernetes("cp1"); got != "kubernetes controlplane: waiting for bootstrap or for the cluster at https://[fd00:100::10]:6443" {
			return fmt.Errorf("status %q", got)
		}
		return nil
	})
	if _, err := c.chalkctl("cp1", "bootstrap", "cp1"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	waitFor(t, 5*time.Minute, "cp1 to hold the VIP", func() error {
		if holders := c.holders("cp1"); len(holders) != 1 {
			return fmt.Errorf("cp1: %s", c.kubernetes("cp1"))
		}
		return nil
	})
	joined := time.Now()
	waitFor(t, 20*time.Minute, "cp2 and cp3 to join etcd", func() error { return c.members("cp1", "cp1", "cp2", "cp3") })
	t.Logf("cp2 and cp3 joined %v after the bootstrap", time.Since(joined).Round(time.Second))
	cs := c.client("cp1")
	waitFor(t, 10*time.Minute, "the three nodes Ready", func() error { return nodesReady(ctx, cs, "cp1", "cp2", "cp3") })
	for _, name := range []string{"cp2", "cp3"} {
		if got := c.kubernetes(name); !strings.HasPrefix(got, "kubernetes controlplane: bootstrapped, node ready: True, vip standby") {
			t.Errorf("status of %s: %q", name, got)
		}
	}
	for _, name := range []string{"cp1", "cp2"} {
		if out, err := c.chalkctl(name, "bootstrap", name); err == nil || !strings.Contains(out, "bootstrapped already") {
			t.Errorf("a second bootstrap of %s was not refused: %v", name, err)
		}
	}
	if holders := c.holders("cp1", "cp2", "cp3"); !slices.Equal(holders, []string{"cp1"}) {
		t.Errorf("VIP holders %v, want cp1", holders)
	}
	c.ipv6Only(ctx, cs)
	p := peers{
		nodes:    [2]string{"cp1", "cp2"},
		nodeIPs:  [2][]string{{"fd00:100::11"}, {"fd00:100::12"}},
		families: []corev1.IPFamily{corev1.IPv6Protocol},
		dnsIP:    "fd00:10:96::a",
	}
	p.create(t, ctx, cs)
	waitFor(t, 10*time.Minute, "pods reaching each other", func() error { return p.reached(ctx, cs, nil) })
	p.check(t, ctx, cs)
	logHAMemory(t, ctx, cs)

	// The VIP moves when its holder's link is cut. The kubelets of the other control planes reach
	// the API server at the endpoint, the VIP, so their leases show it works through the VIP.
	if err := c.nodes["cp1"].vm.SetLink(false); err != nil {
		t.Fatal(err)
	}
	cut := time.Now()
	var holder string
	waitFor(t, 2*time.Minute, "another node to hold the VIP", func() error {
		holders := c.holders("cp2", "cp3")
		if len(holders) != 1 {
			return fmt.Errorf("VIP holders %v", holders)
		}
		holder = holders[0]
		return nil
	})
	movedAfter := time.Since(cut)
	t.Logf("the VIP moved to %s %v after cp1's link was cut", holder, movedAfter.Round(time.Second))
	// The lease lives 10 seconds; the statuses are polled every 5.
	if movedAfter > 30*time.Second {
		t.Errorf("the VIP moved %v after cp1's link was cut, want within 30s", movedAfter.Round(time.Second))
	}
	moved := metav1.Now()
	other := c.client(holder)
	waitFor(t, 5*time.Minute, "the kubelets to renew their leases through the VIP", func() error {
		for _, name := range []string{"cp2", "cp3"} {
			lease, err := other.CoordinationV1().Leases("kube-node-lease").Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if lease.Spec.RenewTime == nil || !lease.Spec.RenewTime.After(moved.Time) {
				return fmt.Errorf("the lease of %s was last renewed %v", name, lease.Spec.RenewTime)
			}
		}
		return nil
	})
	waitFor(t, 2*time.Minute, "cp1 to release the VIP", func() error {
		if got := c.kubernetes("cp1"); strings.HasSuffix(got, "vip holder") {
			return fmt.Errorf("status of cp1: %q", got)
		}
		return nil
	})
	if err := c.nodes["cp1"].vm.SetLink(true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Minute, "cp1 to be a healthy member again", func() error { return c.members(holder, "cp1", "cp2", "cp3") })
	for range 3 {
		time.Sleep(5 * time.Second)
		if holders := c.holders("cp1", "cp2", "cp3"); !slices.Equal(holders, []string{holder}) {
			t.Errorf("VIP holders %v with cp1 back, want %s", holders, holder)
		}
	}

	// cp3 boots without its pinned address: its NIC on the cluster network gets another MAC,
	// which its network configuration does not match.
	c.stop("cp3", false)
	c.start("cp3", "52:54:00:00:02:23", false)
	waitFor(t, 10*time.Minute, "cp3 to fail without its pinned address", func() error {
		want := "kubernetes controlplane: preparation failed: pinned address fd00:100::13 is not present, 30s after chalkos-node-addresses.target; restore it, or remove the node's etcd member with chalkctl etcd remove-member cp3 and reinstall the node"
		if got := c.kubernetes("cp3"); got != want {
			return fmt.Errorf("status of cp3: %q", got)
		}
		return nil
	})
	if _, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err != nil {
		t.Errorf("the API server without cp3: %v", err)
	}
	if out, err := c.chalkctl("cp1", "etcd", "members", "--via", "cp1"); err != nil || !strings.Contains(out, "cp3") {
		t.Errorf("etcd members without cp3: %v\n%s", err, out)
	}
	c.stop("cp3", false)
	c.start("cp3", "52:54:00:00:02:13", false)
	waitFor(t, 10*time.Minute, "cp3 with its address back", func() error { return c.members("cp1", "cp1", "cp2", "cp3") })

	// cp3 leaves etcd, is reinstalled, and joins again.
	if _, err := c.chalkctl("cp3", "etcd", "leave", "cp3"); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if err := c.members("cp1", "cp1", "cp2"); err != nil {
		t.Error(err)
	}
	if got := c.kubernetes("cp3"); got != "kubernetes controlplane: left etcd; reinstall the node to join the cluster again" {
		t.Errorf("status of cp3 after leaving: %q", got)
	}
	c.reinstall("cp3", "52:54:00:00:02:13")
	waitFor(t, 20*time.Minute, "the reinstalled cp3 to join", func() error { return c.members("cp1", "cp1", "cp2", "cp3") })

	// Reinstalled without leaving, cp3 finds its old member and waits until it is removed.
	c.reinstall("cp3", "52:54:00:00:02:13")
	waitFor(t, 10*time.Minute, "cp3 to find its stale member", func() error {
		got := c.kubernetes("cp3")
		if !strings.HasSuffix(got, "etcd has a member cp3 already; remove it with chalkctl etcd remove-member cp3") {
			return fmt.Errorf("status of cp3: %q", got)
		}
		return nil
	})
	// Not yet a member again, cp3 refuses to bootstrap a second cluster: the endpoint answers with
	// this cluster's CA. A join attempt holding the membership refuses it too, so ask again then.
	waitFor(t, time.Minute, "cp3 to refuse a bootstrap while the cluster answers", func() error {
		out, err := c.chalkctl("cp3", "bootstrap", "cp3")
		if err == nil {
			t.Fatalf("cp3 bootstrapped a second cluster:\n%s", out)
		}
		if !strings.Contains(out, "the cluster's API server answers at https://[fd00:100::10]:6443") {
			return fmt.Errorf("bootstrap: %v\n%s", err, out)
		}
		return nil
	})
	if _, err := c.chalkctl("cp1", "etcd", "remove-member", "cp3", "--via", "cp1"); err != nil {
		t.Fatalf("remove-member: %v", err)
	}
	waitFor(t, 20*time.Minute, "cp3 to join after its stale member was removed", func() error { return c.members("cp1", "cp1", "cp2", "cp3") })
	waitFor(t, 10*time.Minute, "the three nodes Ready", func() error { return nodesReady(ctx, cs, "cp1", "cp2", "cp3") })
	logHAMemory(t, ctx, cs)
	t.Logf("the test took %v", time.Since(start).Round(time.Second))
}

// ipv6Only checks that the Nodes and the etcd members have IPv6 addresses alone and that etcd
// and the API server reach each other on IPv6's loopback address.
func (c *haCluster) ipv6Only(ctx context.Context, cs kubernetes.Interface) {
	c.t.Helper()
	for _, hn := range haNodes {
		want := "fd00:100::1" + strings.TrimPrefix(hn.name, "cp")
		if got := internalIPs(c.t, ctx, cs, hn.name); !slices.Equal(got, []string{want}) {
			c.t.Errorf("InternalIPs of %s = %v, want %s", hn.name, got, want)
		}
	}
	out, err := c.chalkctl("cp1", "etcd", "members", "--via", "cp1")
	if err != nil {
		c.t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
		fields := strings.Fields(line)
		if want := "https://[fd00:100::1" + strings.TrimPrefix(fields[0], "cp") + "]:2380"; fields[2] != want {
			c.t.Errorf("etcd member %s has the peer URLs %s, want %s", fields[0], fields[2], want)
		}
	}
	for pod, flags := range map[string][]string{
		"etcd-cp1":           {"--listen-client-urls=https://[::1]:2379,https://[fd00:100::11]:2379", "--listen-metrics-urls=http://[::1]:2381"},
		"kube-apiserver-cp1": {"--etcd-servers=https://[::1]:2379", "--advertise-address=fd00:100::11"},
	} {
		p, err := cs.CoreV1().Pods("kube-system").Get(ctx, pod, metav1.GetOptions{})
		if err != nil {
			c.t.Fatal(err)
		}
		for _, flag := range flags {
			if !slices.Contains(p.Spec.Containers[0].Command, flag) {
				c.t.Errorf("%s runs without %s: %v", pod, flag, p.Spec.Containers[0].Command)
			}
		}
	}
}

// logHAMemory reports the memory each control plane uses.
func logHAMemory(t *testing.T, ctx context.Context, cs kubernetes.Interface) {
	t.Helper()
	for _, hn := range haNodes {
		logNodeMemory(t, ctx, cs, hn.name, haMemoryMB)
	}
}
