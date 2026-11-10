package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/trevex/chalkos/pkg/lab"
)

// The HA cluster's control planes on the switch, as the HA fixture's Network documents match them.
var haNodes = []struct{ name, mac string }{
	{"cp1", "52:54:00:00:02:11"},
	{"cp2", "52:54:00:00:02:12"},
	{"cp3", "52:54:00:00:02:13"},
}

// haMemoryMB is each control plane's memory: three of them fit the local budget of 4.5 GiB.
const haMemoryMB = 1536

// haCluster is the VMs of the HA test.
type haCluster struct {
	// phased holds the running phase's T, which the helpers fail; owner is the whole test, which
	// stops the VMs when it ends.
	phased
	owner    *testing.T
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
	var disks []lab.VMDisk
	if old == nil || fresh {
		disks = []lab.VMDisk{prepareDisk(c.t, dir, diskOpts{imageEnv: "CHALKLAB_K8S_HA_IMAGE_DIR"})}
	} else {
		disks = old.vm.Config.Disks
	}
	apiPort, err := lab.FreePort()
	if err != nil {
		c.t.Fatal(err)
	}
	n := startNode(outliving{c.t, c.owner}, lab.VMConfig{
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

// chalkctl runs chalkctl with the HA cluster's inventory against the node.
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
		if strings.Contains(c.kubernetes(name), ", vip holder") {
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

// client reaches the API server on the node. Its kubeconfig names the port the node's VM forwards
// to the API server, so one written for the VM before serves again.
func (c *haCluster) client(name string) kubernetes.Interface {
	c.t.Helper()
	kubeconfig := filepath.Join(c.dir, fmt.Sprintf("kubeconfig-%s-%d", name, c.nodes[name].apiPort))
	if _, err := os.Stat(kubeconfig); err != nil {
		if _, err := chalkctl(c.t, nil, "ha", "kubeconfig", "--out", kubeconfig, "--server", fmt.Sprintf("https://127.0.0.1:%d", c.nodes[name].apiPort)); err != nil {
			c.t.Fatal(err)
		}
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
// operator removes it. A rolling upgrade with an operator's client file takes two control planes,
// the VIP's holder among them, to a new image one at a time while the cluster keeps answering.
// Each of these phases is a subtest; a failed phase stops the ones after it.
func TestKubernetesHA(t *testing.T) {
	requireEnv(t, append([]string{"CHALKLAB_OVMF_CODE", "CHALKLAB_OVMF_VARS", "CHALKLAB_K8S_HA_IMAGE_DIR", "CHALKLAB_K8S_HA_UPGRADE_IMAGE_DIR", "CHALKLAB_K8S_IMAGES"}, chalkdEnv...)...)
	ctx := context.Background()
	start := time.Now()
	c := &haCluster{phased: phased{t}, owner: t, dir: vmDir(t), nodes: map[string]*haNode{}, boots: map[string]int{}}
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

	var cs kubernetes.Interface
	if !c.phase("bootstrap", func(t *testing.T) {
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
		cs = c.client("cp1")
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
			nodes:        [2]string{"cp1", "cp2"},
			nodeIPs:      [2][]string{{"fd00:100::11"}, {"fd00:100::12"}},
			families:     []corev1.IPFamily{corev1.IPv6Protocol},
			dnsIP:        "fd00:10:96::a",
			kubernetesIP: "fd00:10:96::1",
		}
		p.create(t, ctx, cs)
		waitFor(t, 10*time.Minute, "pods reaching each other", func() error { return p.reached(ctx, cs, nil) })
		p.check(t, ctx, cs)
		logHAMemory(t, ctx, cs)
	}) {
		return
	}

	if !c.phase("vip", func(t *testing.T) {
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
			if got := c.kubernetes("cp1"); strings.Contains(got, ", vip holder") {
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
	}) {
		return
	}

	if !c.phase("pinned address", func(t *testing.T) {
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
	}) {
		return
	}

	if !c.phase("rejoin", func(t *testing.T) {
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
	}) {
		return
	}

	if !c.phase("stale member", func(t *testing.T) {
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
	}) {
		return
	}

	if !c.phase("control plane rollout", func(t *testing.T) {
		c.rolloutControlPlane(ctx, cs)
	}) {
		return
	}

	c.phase("rolling upgrade", func(t *testing.T) {
		c.rollingUpgrade(ctx)
		logHAMemory(t, ctx, cs)
	})
	t.Logf("the test took %v", time.Since(start).Round(time.Second))
}

// ipv6Only checks that the Nodes and the etcd members have IPv6 addresses alone and that etcd
// and the API server reach each other on IPv6's loopback address, and that cp1 takes VXLAN from
// any source without source ranges.
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
	// Without vxlanSourceSubnets VXLAN may come from any source, to cp1's address on the interface
	// holding it, and nothing drops what pods send.
	want := []string{"ip6 daddr fd00:100::11 udp dport 8472 fib daddr . iif type local meta mark set meta mark | 0x01000000"}
	rules, drops := vxlanRules(c.t, c.nodes["cp1"].node, "ha", "cp1")
	if !slices.Equal(rules, want) {
		c.t.Errorf("VXLAN rules of cp1 = %q, want %q", rules, want)
	}
	if len(drops) > 0 {
		c.t.Errorf("VXLAN rules of cp1 for pods = %q, want none", drops)
	}
}

// logHAMemory reports the memory each control plane uses.
func logHAMemory(t *testing.T, ctx context.Context, cs kubernetes.Interface) {
	t.Helper()
	for _, hn := range haNodes {
		logNodeMemory(t, ctx, cs, hn.name, haMemoryMB)
	}
}

// haComponents are the components the control-plane rollout moves to their next patch release:
// the three static pods, then kube-proxy.
var haComponents = []string{"kube-apiserver", "kube-controller-manager", "kube-scheduler", "kube-proxy"}

// imageRollout is the reference of a component the cluster file names and the one the rollout
// replaces it with.
type imageRollout struct{ from, to string }

// haRollout is an imageRollout for each of haComponents. The test registry serves two releases of
// each, which nix/testing/kubernetes-images.nix pins: the one the cluster file names and the
// next patch release.
func haRollout(t *testing.T, served map[string]string, cluster string) []imageRollout {
	t.Helper()
	var rollout []imageRollout
	for _, component := range haComponents {
		var r imageRollout
		for ref := range served {
			if !strings.HasPrefix(ref, "registry.k8s.io/"+component+":") {
				continue
			}
			slot := &r.to
			if strings.Contains(cluster, ref) {
				slot = &r.from
			}
			if *slot != "" {
				t.Fatalf("the test registry serves more than two releases of %s", component)
			}
			*slot = ref
		}
		if r.from == "" || r.to == "" {
			t.Fatalf("the test registry serves no %s that the cluster file names and another to roll out to", component)
		}
		rollout = append(rollout, r)
	}
	return rollout
}

// rolloutControlPlane rolls the control plane out to the next patch release through config: the
// cluster file of the fixtures' copy names the new images, which the later phases read too, and
// config apply delivers it to the three control planes, one at a time in name order, each on trial
// until its static pods run the new images and are Ready. kube-proxy's DaemonSet follows.
func (c *haCluster) rolloutControlPlane(ctx context.Context, cs kubernetes.Interface) {
	t := c.t
	t.Helper()
	data, err := os.ReadFile(os.Getenv("CHALKLAB_K8S_IMAGES"))
	if err != nil {
		t.Fatal(err)
	}
	var served map[string]string
	if err := json.Unmarshal(data, &served); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(inventoryPath("ha")), "cluster", "kubernetes.yaml")
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	rollout := haRollout(t, served, text)
	for _, r := range rollout {
		text = strings.ReplaceAll(text, r.from, r.to)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"config", "apply", "cp1", "cp2", "cp3", "--inventory", inventoryPath("ha")}
	for _, hn := range haNodes {
		args = append(args, "--address", hn.name+"="+c.nodes[hn.name].addr)
	}
	started := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	out, err := chalkctlContext(runCtx, t, nil, "", args...)
	if err != nil {
		t.Fatalf("the rollout: %v", err)
	}
	t.Logf("the control-plane rollout took %v", time.Since(started).Round(time.Second))
	// One control plane at a time, in name order, each confirmed before the next is delivered to.
	last := -1
	for _, hn := range haNodes {
		i := strings.Index(out, hn.name+" keeps config ")
		if i < 0 || i < last {
			t.Errorf("%s was not confirmed, or out of order:\n%s", hn.name, out)
		}
		last = i
	}
	for _, hn := range haNodes {
		for i, r := range rollout[:3] {
			p, err := cs.CoreV1().Pods("kube-system").Get(ctx, haComponents[i]+"-"+hn.name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if got := p.Spec.Containers[0].Image; got != r.to {
				t.Errorf("%s runs %s, want %s", p.Name, got, r.to)
			}
		}
	}
	waitFor(t, 5*time.Minute, "kube-proxy on the new image", func() error {
		ds, err := cs.AppsV1().DaemonSets("kube-system").Get(ctx, "kube-proxy", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if got := ds.Spec.Template.Spec.Containers[0].Image; got != rollout[3].to {
			return fmt.Errorf("kube-proxy's DaemonSet names %s", got)
		}
		if s := ds.Status; s.UpdatedNumberScheduled != s.DesiredNumberScheduled || s.NumberAvailable != s.DesiredNumberScheduled {
			return fmt.Errorf("kube-proxy: %d of %d updated, %d available", s.UpdatedNumberScheduled, s.DesiredNumberScheduled, s.NumberAvailable)
		}
		return nil
	})
	waitFor(t, 5*time.Minute, "the three nodes Ready", func() error { return nodesReady(ctx, cs, "cp1", "cp2", "cp3") })
	// A renewal still failing would render the static pods again on its retry, restarting etcd
	// while the next phase reboots another control plane.
	waitFor(t, 5*time.Minute, "no renewal failing", func() error {
		for _, hn := range haNodes {
			out, err := c.chalkctl(hn.name, "status", hn.name)
			if err != nil {
				return err
			}
			if strings.Contains(out, "renewal failing") {
				return fmt.Errorf("a renewal of %s fails:\n%s", hn.name, out)
			}
		}
		return nil
	})
	// Status reports the API server of the config next to the image's kubelet, a patch behind it.
	want := "kubernetes versions: API server 1.37.2, kubelet 1.37.1, control-plane pods current\n"
	if out, err := c.chalkctl("cp1", "status", "cp1"); err != nil || !strings.Contains(out, want) {
		t.Errorf("cp1's status lacks %q: %v\n%s", want, err, out)
	}
}

// rollingUpgrade upgrades two control planes to the HA upgrade image with an operator's client
// file: the VIP's holder, so the VIP moves, and one other, one at a time. Meanwhile an API server
// answers every five seconds, and no Node is other than Ready but the one chalkctl reboots, from
// "rebooting into" until it found it healthy again. The upgraded ones boot the image as
// chalkos_2.efi; the third keeps chalkos_1.efi.
func (c *haCluster) rollingUpgrade(ctx context.Context) {
	t := c.t
	t.Helper()
	base, next := rootHash(t, "CHALKLAB_K8S_HA_IMAGE_DIR")[:12], rootHash(t, "CHALKLAB_K8S_HA_UPGRADE_IMAGE_DIR")[:12]
	all := []string{"cp1", "cp2", "cp3"}
	holders := c.holders(all...)
	if len(holders) != 1 {
		t.Fatalf("VIP holders %v before the upgrade, want one", holders)
	}
	upgraded := []string{holders[0]}
	var kept string
	for _, name := range all {
		switch {
		case name == holders[0]:
		case len(upgraded) == 1:
			upgraded = append(upgraded, name)
		default:
			kept = name
		}
	}
	slices.Sort(upgraded)
	t.Logf("upgrading %v; %s holds the VIP", upgraded, holders[0])
	config := filepath.Join(c.dir, "operator.json")
	if _, err := chalkctl(t, nil, "", "client", "new", "--name", "ops", "--role", "operator", "--out", config); err != nil {
		t.Fatal(err)
	}
	clients := map[string]kubernetes.Interface{}
	for _, name := range all {
		clients[name] = c.client(name)
	}

	// reboots holds, by node, when chalkctl said it reboots it and when it found it healthy again.
	type window struct{ from, to time.Time }
	var mu sync.Mutex
	reboots := map[string]*window{}
	rebooting := func(name string, at time.Time) bool {
		mu.Lock()
		defer mu.Unlock()
		w := reboots[name]
		return w != nil && !at.Before(w.from) && (w.to.IsZero() || !at.After(w.to))
	}

	// Every five seconds, from any API server that answers: no Node but a rebooting one may be
	// other than Ready. The longest each other node's lease went unrenewed is logged.
	stop := make(chan struct{})
	var problems []string
	longest := map[string]time.Duration{}
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		silentSince := time.Time{}
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Second):
			}
			var notReady []string
			answered := false
			for _, name := range all {
				sampled := time.Now()
				lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
				nodes, err := clients[name].CoreV1().Nodes().List(lctx, metav1.ListOptions{})
				var leases *coordinationv1.LeaseList
				if err == nil {
					leases, err = clients[name].CoordinationV1().Leases("kube-node-lease").List(lctx, metav1.ListOptions{})
				}
				cancel()
				if err != nil {
					continue
				}
				answered = true
				for _, n := range nodes.Items {
					ready := false
					for _, cond := range n.Status.Conditions {
						ready = ready || cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue
					}
					if !ready && !rebooting(n.Name, sampled) {
						notReady = append(notReady, n.Name)
					}
				}
				for _, l := range leases.Items {
					if l.Spec.RenewTime != nil && !rebooting(l.Name, sampled) {
						longest[l.Name] = max(longest[l.Name], sampled.Sub(l.Spec.RenewTime.Time).Round(time.Second))
					}
				}
				break
			}
			switch {
			case !answered && silentSince.IsZero():
				silentSince = time.Now()
			case !answered && time.Since(silentSince) > 30*time.Second:
				problems = append(problems, fmt.Sprintf("no API server answered for %v", time.Since(silentSince).Round(time.Second)))
			case answered:
				silentSince = time.Time{}
				if len(notReady) > 0 {
					problems = append(problems, fmt.Sprintf("the nodes %v, which were not rebooting, are not Ready", notReady))
				}
			}
		}
	}()
	args := []string{"upgrade", "--image", os.Getenv("CHALKLAB_K8S_HA_UPGRADE_IMAGE_DIR"), "--config", config, "--nodes", strings.Join(upgraded, ","),
		"--inventory", inventoryPath("ha"), "--timeout", "15m"}
	for _, name := range all {
		args = append(args, "--address", name+"="+c.nodes[name].addr)
	}
	started := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, 25*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(runCtx, os.Getenv("CHALKLAB_CHALKCTL"), args...)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	var out strings.Builder
	var order []string
	read := make(chan struct{})
	go func() {
		defer close(read)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			out.WriteString(line + "\n")
			now := time.Now()
			mu.Lock()
			if name, ok := strings.CutSuffix(line, ": rebooting into "+next); ok {
				reboots[name] = &window{from: now}
				order = append(order, name)
			}
			if name, ok := strings.CutSuffix(line, ": runs "+next+", found healthy"); ok && reboots[name] != nil {
				reboots[name].to = now
			}
			mu.Unlock()
		}
		io.Copy(io.Discard, pr)
	}()
	err := cmd.Run()
	pw.Close()
	<-read
	t.Logf("chalkctl %s:\n%s", strings.Join(args, " "), out.String())
	close(stop)
	<-watched
	t.Logf("the longest the leases of nodes not rebooting went unrenewed: %v", longest)
	if err != nil {
		t.Fatalf("the rolling upgrade: %v", err)
	}
	t.Logf("the rolling upgrade took %v", time.Since(started).Round(time.Second))
	for _, p := range problems {
		t.Errorf("during the rolling upgrade %s", p)
	}
	// The control planes went one at a time, in name order.
	if !slices.Equal(order, upgraded) {
		t.Errorf("the control planes rebooted in the order %v, want %v", order, upgraded)
	}
	if err := c.members(upgraded[0], all...); err != nil {
		t.Error(err)
	}
	for _, name := range upgraded {
		if out, err := c.chalkctl(name, "status", name); err != nil || !strings.Contains(out, "image "+next+", booted from chalkos_2.efi\n") {
			t.Errorf("status of %s after the upgrade: %v", name, err)
		}
	}
	if out, err := c.chalkctl(kept, "status", kept); err != nil || !strings.Contains(out, "image "+base+", booted from chalkos_1.efi\n") {
		t.Errorf("status of %s, which the upgrade left out: %v", kept, err)
	}
	if holders := c.holders(all...); len(holders) != 1 {
		t.Errorf("VIP holders %v after the upgrade, want one", holders)
	}
}
