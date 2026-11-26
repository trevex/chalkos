package e2e

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v4"
	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/trevex/chalkos/pkg/lab"
)

// labName is the metadata.name of the main cluster's lab file, chalklab/lab.yaml, by which
// chalklab keys the lab's state directory.
const labName = "chalklab"

// labMemory is the memory in MiB of the VM of each node the main cluster's lab file runs, as the
// lab file gives it.
func labMemory(t *testing.T) map[string]int {
	t.Helper()
	path := filepath.Join(fixtures, "chalklab", "lab.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lf struct {
		Spec struct {
			Nodes map[string]struct {
				Memory int `yaml:"memory"`
			} `yaml:"nodes"`
		} `yaml:"spec"`
	}
	if err := yaml.Load(data, &lf); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	memory := map[string]int{}
	for name, n := range lf.Spec.Nodes {
		if n.Memory == 0 {
			t.Fatalf("%s gives %s no memory, which the test's budget sets", path, name)
		}
		memory[name] = n.Memory
	}
	return memory
}

// clientsetOf is a client of the cluster of the kubeconfig at path, and its config.
func clientsetOf(t testing.TB, path string) (kubernetes.Interface, *rest.Config) {
	t.Helper()
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return cs, cfg
}

// probeDoneRE is the probe's last fact on the console, written once a boot is up.
var probeDoneRE = regexp.MustCompile(`CHALKTEST done=1`)

// TestKubernetesCluster brings up through chalklab a dual-stack cluster, IPv4 primary, of a
// control plane and a worker on KVM, and runs its phases in order; a failed phase stops the rest.
// The images come from a registry the test serves; with CHALKLAB_K8S_ONLINE=1 it serves none, so
// the nodes' mirror is unreachable and containerd pulls from upstream. Offline, cp1 then serves
// the cluster registry, which w1 pulls from, and the renewals check that it serves cp1's renewed
// node certificate.
func TestKubernetesCluster(t *testing.T) {
	requireEnv(t, append([]string{"CHALKLAB_CHALKLAB", "CHALKLAB_K8S_IMAGE_DIR", "CHALKLAB_OUTSIDE_MODULE"}, chalkdEnv...)...)
	online := os.Getenv("CHALKLAB_K8S_ONLINE") == "1"
	if !online {
		requireEnv(t, "CHALKLAB_K8S_IMAGES")
	}
	dir := vmDir(t)
	ctx := context.Background()

	var args []string
	if !online {
		args = []string{"--guest-forward", "10.0.2.100:5000=" + startRegistry(t)}
	}
	startNTP(t)
	nodes, apiPort, registryPort, kubeconfig := createLab(t, dir, args...)
	cs, _ := clientsetOf(t, kubeconfig)
	// The checks of the nodes and of the cluster's networking, again after cp1's reboot. cp1 has
	// fixed addresses; w1 picks the ones in its validSubnets.
	checkNetworking := func(t *testing.T) {
		for _, name := range []string{"cp1", "w1"} {
			dualStackNode(t, ctx, cs, nodes[name], name)
		}
	}
	p := peers{
		nodes:        [2]string{"cp1", "w1"},
		nodeIPs:      [2][]string{dualStackAddresses["cp1"], dualStackAddresses["w1"]},
		families:     []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol},
		dnsIP:        "10.96.0.10",
		kubernetesIP: "10.96.0.1",
	}

	if !t.Run("ready", func(t *testing.T) {
		for _, name := range []string{"cp1", "w1"} {
			waitFor(t, 5*time.Minute, "the guest agent of "+name, func() error {
				ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				return nodes[name].vm.PingGuestAgent(ctx)
			})
		}
		waitFor(t, 15*time.Minute, "both nodes Ready", func() error { return nodesReady(ctx, cs, "cp1", "w1") })
		waitFor(t, 5*time.Minute, "approved kubelet serving certificates", func() error { return servingCertificates(ctx, cs, "cp1", "w1") })
		if out, err := chalkctl(t, nodes["w1"], "base", "status", "w1"); err != nil || !strings.Contains(out, "kubernetes worker: joined, node ready: True") || strings.Contains(out, "pods unknown") {
			t.Errorf("status of w1: %v\n%s", err, out)
		}
		for _, name := range []string{"cp1", "w1"} {
			synchronised(t, nodes[name], "base", name, testNTP)
			if out, err := chalkctl(t, nodes[name], "base", "status", name); err != nil || strings.Contains(out, "failed unit") {
				t.Errorf("status of %s shows a failed unit: %v\n%s", name, err, out)
			}
		}
		readerClientFile(t, nodes["w1"], filepath.Join(dir, "reader.json"))
		checkNetworking(t)
		if !online {
			privatePull(t, ctx, cs)
		}
	}) {
		return
	}

	if !t.Run("pods", func(t *testing.T) {
		p.create(t, ctx, cs)
		start := time.Now()
		waitFor(t, 10*time.Minute, "pods reaching each other", func() error { return p.reached(ctx, cs, nil) })
		t.Logf("pods reached each other after %v", time.Since(start).Round(time.Second))
		p.check(t, ctx, cs)
		execPaths(t, ctx, cs)
		logMemory(t, ctx, cs)
	}) {
		return
	}

	if !t.Run("host-integration", func(t *testing.T) {
		modulePolicy(t, ctx, cs, nodes)
		longhornTools(t, ctx, cs)
		modulePolicyTampered(t, ctx, cs, nodes)
	}) {
		return
	}

	if !t.Run("cluster-registry", func(t *testing.T) { clusterRegistry(t, ctx, cs, nodes, registryPort, online) }) {
		return
	}

	if !t.Run("reboot", func(t *testing.T) {
		before := servingCertificate(t, apiPort)
		// The probe's last fact on the console after the reboot marks the new boot; until then
		// the old chalkd may still answer.
		cp1 := nodes["cp1"]
		cp1.vm.Console.Skip()
		requested := time.Now()
		if _, err := chalkctl(t, cp1, "base", "node", "reboot", "cp1", "--no-drain"); err != nil {
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
		// flannel announces its addresses when it starts, so the annotations are this boot's once
		// it started again.
		waitFor(t, 5*time.Minute, "flannel on cp1 to start after the reboot", func() error { return flannelStarted(ctx, cs, "cp1", requested) })
		checkNetworking(t)
		p.check(t, ctx, cs)
		logMemory(t, ctx, cs)
	}) {
		return
	}

	if !t.Run("renewals", func(t *testing.T) { renewals(t, ctx, cs, nodes, apiPort, registryPort, online) }) {
		return
	}

	// Revoking w1 deletes its Node, and with it the pod the rotations' checks need: it comes after
	// them.
	// The rotated secrets outlive the rotations phase: the later phases' chalkctl calls read them.
	secrets := copySecrets(t, dir)
	t.Setenv("CHALKLAB_SECRETS", secrets)
	var rotated string
	if !t.Run("rotations", func(t *testing.T) { rotated = rotations(t, ctx, nodes, p, dir, secrets, apiPort) }) {
		return
	}
	if !t.Run("revocation", func(t *testing.T) { revocation(t, ctx, nodes, rotated) }) {
		return
	}

	t.Run("restart", func(t *testing.T) {
		// The host shuts the worker down through its guest agent, and its VM exits.
		w1 := nodes["w1"]
		shutdownCtx, cancelShutdown := context.WithTimeout(ctx, time.Minute)
		defer cancelShutdown()
		if err := w1.vm.ShutdownGuest(shutdownCtx); err != nil {
			t.Fatal(err)
		}
		waitFor(t, 3*time.Minute, "the VM of w1 to exit once its guest agent shut it down", func() error {
			if lab.Running(w1.vm.Config) {
				return errors.New("the VM of w1 runs")
			}
			return nil
		})
		// The lab runs on without w1, and chalklab start has its supervisor start w1's VM alone
		// again.
		if !lab.Running(nodes["cp1"].vm.Config) {
			t.Error("the VM of cp1 stopped with w1's")
		}
		startCtx, cancelStart := context.WithTimeout(ctx, 3*time.Minute)
		defer cancelStart()
		out, err := runLogged(t, exec.CommandContext(startCtx, os.Getenv("CHALKLAB_CHALKLAB"), "start"))
		if err != nil {
			t.Fatalf("chalklab start: %v", err)
		}
		if !strings.Contains(out, "starting w1\n") {
			t.Error("chalklab start did not start w1 alone")
		}
		if !lab.Running(w1.vm.Config) || !lab.Running(nodes["cp1"].vm.Config) {
			t.Error("the VMs of cp1 and w1 do not both run after chalklab start")
		}
	})
}

// revocation revokes w1 through cp1, after which cp1 lists it as revoked, refuses to renew its
// node certificate and the API server denies its kubelet's writes; the test impersonates the
// kubelet as the kubeconfig's admin. Unrevoking w1 is refused, as the OS CA and the Kubernetes
// CA predate the revocation, until --allow-old-ca. A kubelet registers its Node once, when it
// starts: w1 reboots to register again, and turns Ready.
func revocation(t *testing.T, ctx context.Context, nodes map[string]*node, kubeconfig string) {
	t.Helper()
	w1, cp1 := nodes["w1"], nodes["cp1"]
	cs, cfg := clientsetOf(t, kubeconfig)
	start := time.Now()
	if out, err := chalkctl(t, cp1, "base", "node", "revoke", "w1", "--via", "cp1"); err != nil || !strings.Contains(out, "deleted its Node object") {
		t.Fatalf("revoke w1: %v", err)
	}
	if out, err := chalkctl(t, cp1, "base", "status", "cp1"); err != nil || !strings.Contains(out, "revoked nodes:\n  w1  since ") {
		t.Errorf("status of cp1 does not list w1 as revoked: %v", err)
	}
	// The test image renews after each ApplyConfig.
	if _, err := chalkctl(t, w1, "base", "config", "apply", "w1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Minute, "w1's renewal refused as revoked", func() error {
		out, err := chalkctl(t, w1, "base", "status", "w1")
		if err != nil {
			return err
		}
		if !strings.Contains(out, "renewal failing: ") || !strings.Contains(out, "node w1 is revoked since ") {
			return errors.New("w1 shows no refused renewal")
		}
		return nil
	})

	kubelet := rest.CopyConfig(cfg)
	kubelet.Impersonate = rest.ImpersonationConfig{UserName: "system:node:w1", Groups: []string{"system:nodes", "system:authenticated"}}
	kcs, err := kubernetes.NewForConfig(kubelet)
	if err != nil {
		t.Fatal(err)
	}
	// The API server picks up the policy's change asynchronously, within seconds; a dry run
	// passes admission without registering the Node.
	waitFor(t, time.Minute, "the API server to deny w1's writes", func() error {
		_, err := kcs.CoreV1().Nodes().Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w1"}}, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if !apierrors.IsForbidden(err) {
			return fmt.Errorf("registering w1's Node: %s", describe(err))
		}
		return nil
	})
	_, err = kcs.CoreV1().Nodes().Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w1"}}, metav1.CreateOptions{})
	t.Logf("w1's kubelet registering its Node: %s", describe(err))
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "node w1 is revoked since ") {
		t.Errorf("w1's kubelet registering its Node: %s, want denied as revoked", describe(err))
	}

	out, err := chalkctl(t, cp1, "base", "node", "unrevoke", "w1", "--via", "cp1")
	if err == nil || !strings.Contains(out, "chalkctl rotate os-ca") || !strings.Contains(out, "chalkctl rotate kubernetes-ca") {
		t.Errorf("unrevoking w1 without --allow-old-ca: %v, want refused naming os-ca and kubernetes-ca", err)
	}
	if _, err := chalkctl(t, cp1, "base", "node", "unrevoke", "w1", "--via", "cp1", "--allow-old-ca"); err != nil {
		t.Fatalf("unrevoke w1 --allow-old-ca: %v", err)
	}
	if _, err := cs.CoreV1().Nodes().Get(ctx, "w1", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("w1's Node after the unrevoke, before w1 rebooted: %s, want not found", describe(err))
	}
	w1.vm.Console.Skip()
	if _, err := chalkctl(t, w1, "base", "node", "reboot", "w1", "--no-drain"); err != nil {
		t.Fatal(err)
	}
	rebootCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if _, err := w1.vm.Console.WaitFor(rebootCtx, probeDoneRE); err != nil {
		t.Fatalf("w1 did not boot again: %v", err)
	}
	waitFor(t, 10*time.Minute, "w1 Ready after the unrevoke", func() error { return nodesReady(ctx, cs, "w1") })
	t.Logf("w1 was revoked and Ready again after %v", time.Since(start).Round(time.Second))
}

// privatePull runs on w1 a pod of privateImage without imagePullSecrets, which only the node's
// credentials from its Registry pull: the image's host resolves nowhere, and its Mirror's
// endpoint asks for credentials and has no Registry credentials of its own. The pod succeeds
// only if the kubelet's credential provider answers and containerd sends the image host's
// credentials to the mirror endpoint, as containerd 2.4 does.
func privatePull(t *testing.T, ctx context.Context, cs kubernetes.Interface) {
	t.Helper()
	podSucceeds(t, ctx, cs, "private", privateImage)
}

// execScript checks where the node runs programs from. runc mounts a pod's host paths with flags of
// its own, so the script runs its copies of busybox in the node's mount namespace, with nsenter,
// where the node's flags apply. The image's busybox needs a dynamic loader the node lacks, so a
// copy the kernel lets run fails with ENOENT, exit status 127, after the mount's noexec check;
// one on a noexec mount fails with EACCES, exit status 126, before it.
//   - /opt/cni/bin, where a CNI DaemonSet installs the plugins containerd runs, allows it; the
//     directory is a tmpfs, and flannel's configuration is found through /etc/cni/net.d;
//   - a disk-backed emptyDir, which lies below /var/lib/kubelet, an exec path, allows it;
//   - /var otherwise, which is noexec, does not.
const execScript = `set -ex
grep -q '^tmpfs /cni tmpfs ' /proc/mounts
test -f /netd/10-flannel.conflist
cp /bin/busybox /cni/busybox-chalkos-e2e
cp /bin/busybox /scratch/busybox
cp /bin/busybox /var-noexec/busybox
trap 'rm -f /cni/busybox-chalkos-e2e /var-noexec/busybox' EXIT
# The exit status and message of running the file on the node.
run() {
  status=0
  nsenter -t 1 -m -- "$1" true 2>/tmp/err || status=$?
  echo "$status $(cat /tmp/err)"
}
runs() {
  case "$(run "$1")" in
  0*|"127 "*"No such file or directory"*) ;;
  *) echo "$1 does not run: $(run "$1")"; exit 1 ;;
  esac
}
runs /opt/cni/bin/busybox-chalkos-e2e
runs "/var/lib/kubelet/pods/$POD_UID/volumes/kubernetes.io~empty-dir/scratch/busybox"
case "$(run /var/lib/chalkos-e2e-noexec/busybox)" in
"126 "*"Permission denied"*) ;;
*) echo "a program below /var ran or failed otherwise: $(run /var/lib/chalkos-e2e-noexec/busybox)"; exit 1 ;;
esac
`

// execPaths runs execScript on w1 in a privileged pod in the node's PID namespace that mounts the
// node's /opt/cni/bin and /etc/cni/net.d by hostPath, as a CNI DaemonSet does, a directory below
// /var by hostPath and an emptyDir, and waits for it to succeed.
func execPaths(t *testing.T, ctx context.Context, cs kubernetes.Interface) {
	t.Helper()
	hostPath := func(name, path string, kind corev1.HostPathType) corev1.Volume {
		return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: path, Type: &kind}}}
	}
	privileged := true
	_, err := cs.CoreV1().Pods("default").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "exec-paths"},
		Spec: corev1.PodSpec{
			NodeSelector:  map[string]string{"kubernetes.io/hostname": "w1"},
			RestartPolicy: corev1.RestartPolicyNever,
			HostPID:       true,
			Volumes: []corev1.Volume{
				hostPath("cni", "/opt/cni/bin", corev1.HostPathUnset),
				hostPath("netd", "/etc/cni/net.d", corev1.HostPathUnset),
				hostPath("var-noexec", "/var/lib/chalkos-e2e-noexec", corev1.HostPathDirectoryOrCreate),
				{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
			Containers: []corev1.Container{{
				Name:            "exec-paths",
				Image:           "docker.io/library/busybox:1.37.0",
				Command:         []string{"sh", "-c", execScript},
				SecurityContext: &corev1.SecurityContext{Privileged: &privileged},
				Env: []corev1.EnvVar{{Name: "POD_UID", ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"},
				}}},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "cni", MountPath: "/cni"},
					{Name: "netd", MountPath: "/netd", ReadOnly: true},
					{Name: "var-noexec", MountPath: "/var-noexec"},
					{Name: "scratch", MountPath: "/scratch"},
				},
			}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Minute, "the pod that runs programs from the node's exec paths", func() error {
		pod, err := cs.CoreV1().Pods("default").Get(ctx, "exec-paths", metav1.GetOptions{})
		if err != nil {
			return err
		}
		switch pod.Status.Phase {
		case corev1.PodSucceeded:
			return nil
		case corev1.PodFailed:
			logs, lerr := cs.CoreV1().Pods("default").GetLogs("exec-paths", &corev1.PodLogOptions{}).DoRaw(ctx)
			t.Fatalf("the pod failed: %v\nits log (%v):\n%s", pod.Status.ContainerStatuses, lerr, logs)
		}
		return fmt.Errorf("the pod is %s", pod.Status.Phase)
	})
}

// podSucceeds runs true in a pod of the name and the image on w1, without pull secrets, and waits
// for it to succeed. It creates the pod again until the API server has it, as a delivery to cp1
// just before may restart the API server with renewed certificates.
func podSucceeds(t *testing.T, ctx context.Context, cs kubernetes.Interface, name, image string) {
	t.Helper()
	waitFor(t, 5*time.Minute, "the pod of "+image, func() error {
		pod, err := cs.CoreV1().Pods("default").Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			pod, err = cs.CoreV1().Pods("default").Create(ctx, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec: corev1.PodSpec{
					NodeSelector:  map[string]string{"kubernetes.io/hostname": "w1"},
					RestartPolicy: corev1.RestartPolicyNever,
					Containers:    []corev1.Container{{Name: name, Image: image, Command: []string{"true"}}},
				},
			}, metav1.CreateOptions{})
		}
		if err != nil {
			return err
		}
		if len(pod.Spec.ImagePullSecrets) > 0 {
			t.Fatalf("the pod has pull secrets %v", pod.Spec.ImagePullSecrets)
		}
		if pod.Status.Phase != corev1.PodSucceeded {
			return fmt.Errorf("the pod is %s: %v", pod.Status.Phase, pod.Status.ContainerStatuses)
		}
		return nil
	})
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

// logMemory reports the memory each node uses, as its kubelet measures it.
func logMemory(t *testing.T, ctx context.Context, cs kubernetes.Interface) {
	t.Helper()
	for name, memoryMB := range labMemory(t) {
		logNodeMemory(t, ctx, cs, name, memoryMB)
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
	cert, err := peerCertificate(fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	return cert
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
// preparation logged them last in this boot; inventory names the cluster's inventory, base or ha.
func vxlanRules(t *testing.T, n *node, inventory, name string) (marks, drops []string) {
	t.Helper()
	out, err := chalkctl(t, n, inventory, "logs", name, "--unit", "chalkos-kubernetes")
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

// createLab creates the main cluster's lab of chalklab/lab.yaml through startLab; chalklab
// bootstraps cp1, and createLab returns the nodes, the forwarded ports of cp1's API server and
// of its cluster registry, and the lab's kubeconfig. chalklab printed the endpoint cp1 waited for,
// and chalkctl reaches cp1 with the lab's inventory and client file.
func createLab(t *testing.T, dir string, args ...string) (nodes map[string]*node, apiPort, registryPort int, kubeconfig string) {
	t.Helper()
	nodes, l, out, state := startLab(t, dir, labName, "lab.yaml", args...)
	// The status chalklab saw before the bootstrap names the cluster's endpoint.
	if want := "cp1: kubernetes controlplane: waiting for the cluster at https://192.168.100.11:6443; bootstrap only the first control plane of a new cluster"; !strings.Contains(out, want) {
		t.Errorf("chalklab create did not print %q", want)
	}
	for _, n := range l.Nodes {
		if n.APIPort != 0 {
			apiPort = n.APIPort
		}
		for _, f := range n.Forwards {
			if n.Name == "cp1" && f.Guest == clusterRegistryPort {
				registryPort = f.Host
			}
		}
	}
	if registryPort == 0 {
		t.Fatalf("the lab forwards no port to cp1's cluster registry, port %d", clusterRegistryPort)
	}
	if out, err := runChalklab(t, "status"); err != nil {
		t.Errorf("chalklab status: %v", err)
	} else if !strings.Contains(out, "its supervisor runs") {
		t.Error("chalklab status does not say that its supervisor runs the lab")
	} else if forward := fmt.Sprintf("%d=127.0.0.1:%d", clusterRegistryPort, registryPort); !strings.Contains(out, forward) {
		t.Errorf("chalklab status does not list cp1's forward %s", forward)
	}
	// The lab's inventory names cp1 at its forwarded port, not at its address on the cluster
	// network, which the host does not reach.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	statusOut, err := runLogged(t, exec.CommandContext(ctx, os.Getenv("CHALKLAB_CHALKCTL"), "status", "cp1", "--config", filepath.Join(state, "chalkctl.json"), "--inventory", filepath.Join(state, "chalkos.yaml")))
	if err != nil {
		t.Errorf("chalkctl status with the lab's inventory and client file: %v", err)
	} else if !strings.Contains(statusOut, "kubernetes controlplane: ") {
		t.Error("chalkctl status with the lab's inventory and client file shows no Kubernetes status of cp1")
	}
	return nodes, apiPort, registryPort, filepath.Join(state, "kubeconfig")
}
