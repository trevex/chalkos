package e2e

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// hostPod runs script with busybox's sh on w1 in a privileged pod in the node's PID namespace,
// with the hostPath volumes given by mount point, and waits for it to succeed.
func hostPod(t *testing.T, ctx context.Context, cs kubernetes.Interface, name, script string, hostPaths map[string]string, extra ...corev1.Volume) {
	t.Helper()
	privileged := true
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount
	i := 0
	for mount, path := range hostPaths {
		v := fmt.Sprintf("host%d", i)
		i++
		kind := corev1.HostPathUnset
		volumes = append(volumes, corev1.Volume{Name: v, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: path, Type: &kind}}})
		mounts = append(mounts, corev1.VolumeMount{Name: v, MountPath: mount})
	}
	for _, v := range extra {
		volumes = append(volumes, v)
		mounts = append(mounts, corev1.VolumeMount{Name: v.Name, MountPath: "/" + v.Name})
	}
	_, err := cs.CoreV1().Pods("default").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PodSpec{
			NodeSelector:  map[string]string{"kubernetes.io/hostname": "w1"},
			RestartPolicy: corev1.RestartPolicyNever,
			HostPID:       true,
			Volumes:       volumes,
			Containers: []corev1.Container{{
				Name:            name,
				Image:           "docker.io/library/busybox:1.37.0",
				Command:         []string{"sh", "-c", script},
				SecurityContext: &corev1.SecurityContext{Privileged: &privileged},
				VolumeMounts:    mounts,
			}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Minute, "the pod "+name, func() error {
		pod, err := cs.CoreV1().Pods("default").Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		switch pod.Status.Phase {
		case corev1.PodSucceeded:
			return nil
		case corev1.PodFailed:
			logs, lerr := cs.CoreV1().Pods("default").GetLogs(name, &corev1.PodLogOptions{}).DoRaw(ctx)
			t.Fatalf("the pod %s failed: %v\nits log (%v):\n%s", name, pod.Status.ContainerStatuses, lerr, logs)
		}
		return fmt.Errorf("the pod is %s", pod.Status.Phase)
	})
}

// policyScript runs in a privileged pod: it holds neither CAP_SYS_MODULE nor CAP_MAC_ADMIN, so it
// can load no module, not even one of the image's store, and cannot switch IPE to permissive.
// It checks the modules the ceph and kubevirt features give the host, and then hands the module
// from outside the image to the host.
const policyScript = `
set -u
fail() { echo "$*"; exit 1; }
bnd=$(sed -n 's/^CapBnd:[[:space:]]*//p' /proc/self/status)
[ $(( (0x$bnd >> 16) & 1 )) = 0 ] || fail "the pod's bounding set $bnd holds CAP_SYS_MODULE"
[ $(( (0x$bnd >> 33) & 1 )) = 0 ] || fail "the pod's bounding set $bnd holds CAP_MAC_ADMIN"
# The host's /lib/modules, mounted as charts mount it, holds the image's modules as files, not as
# links into the store, which the pod does not see.
l=$(find /lib/modules/ -type l)
[ -z "$l" ] || fail "the host's /lib/modules holds links: $l"
[ -s /lib/modules/$(uname -r)/modules.dep ] || fail "the host's /lib/modules has no modules.dep: $(ls -la /lib/modules/ 2>&1)"
m=$(find /lib/modules/$(uname -r)/ -name '*.ko*' | head -n 1)
[ -n "$m" ] || fail "the image's module tree has no module: $(ls -la /lib/modules/ 2>&1)"
# ceph loads rbd, ceph and nbd at boot; kubevirt carries KVM's, vhost's and VFIO's modules, which
# the kernel loads on demand. /dev/kvm exists when the CPU offers virtualisation, which a VM on a
# host without nested virtualisation does not.
for k in rbd ceph nbd; do [ -d /sys/module/$k ] || fail "$k is not loaded"; done
for k in kvm kvm[-_]intel kvm[-_]amd vhost[-_]net vhost[-_]vsock vfio vfio[-_]pci uio; do
  [ -n "$(find /lib/modules/$(uname -r)/ -name "$k.ko*" | head -n 1)" ] || fail "the image's module tree has no $k"
done
if grep -qwE 'vmx|svm' /proc/cpuinfo; then
  [ -c /dev/kvm ] || fail "the CPU offers virtualisation, but there is no /dev/kvm"
else
  echo "the CPU offers no virtualisation: /dev/kvm is not checked"
fi
if out=$(insmod "$m" 2>&1); then fail "the pod loaded $m"; fi
case "$out" in *"not permitted"*) ;; *) fail "insmod of $m failed otherwise: $out" ;; esac
if out=$( (echo 0 > /securityfs/ipe/enforce) 2>&1 ); then fail "the pod switched IPE to permissive"; fi
case "$out" in *"not permitted"*) ;; *) fail "the write to ipe/enforce failed otherwise: $out" ;; esac
[ "$(cat /securityfs/ipe/enforce)" = 1 ] || fail "IPE is permissive"
cp /outside/chalkos_e2e_outside.ko /drop/outside.ko
touch /drop/load
`

var moduleOutsideRE = regexp.MustCompile(`CHALKTEST module_outside=(\S+)(?: module_outside_ipe=(\d))?`)

// modulePolicy checks w1's module policy: a privileged pod can neither load a module of the
// image's store nor switch IPE to permissive; the host's attempt to load the module from outside
// the image is refused by IPE; and chalkctl status shows the policy as enforced.
func modulePolicy(t *testing.T, ctx context.Context, cs kubernetes.Interface, nodes map[string]*node) {
	t.Helper()
	ko, err := os.ReadFile(os.Getenv("CHALKLAB_OUTSIDE_MODULE"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().ConfigMaps("default").Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "outside"},
		BinaryData: map[string][]byte{"chalkos_e2e_outside.ko": ko},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	hostPod(t, ctx, cs, "module-policy", policyScript, map[string]string{
		"/lib/modules": "/lib/modules",
		"/securityfs":  "/sys/kernel/security",
		"/drop":        "/var/lib/chalktest-modules",
	}, corev1.Volume{Name: "outside", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "outside"}}}})

	wctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	m, err := nodes["w1"].vm.Console.WaitFor(wctx, moduleOutsideRE)
	if err != nil {
		t.Fatalf("waiting for w1's attempt to load the module from outside the image: %v", err)
	}
	if m[1] != "refused" || m[2] != "1" {
		t.Errorf("w1's kernel did not refuse the module from outside the image through IPE: %s", m[0])
	}
	if out, err := chalkctl(t, nodes["w1"], "base", "status", "w1"); err != nil || !strings.Contains(out, "module policy: enforced") {
		t.Errorf("status of w1: %v\n%s", err, out)
	}
}

// longhornScript checks what the longhorn feature gives w1, as Longhorn uses it. Longhorn runs
// iscsiadm in the mount and network namespaces of the processes named iscsid, which must be the
// host's, and finds it through its PATH; a discovery that reaches no target shows that iscsiadm
// got the node's initiator name from iscsid. Longhorn runs fstrim, cryptsetup and dmsetup in the
// host's mount namespace, and its CSI plugin mounts RWX volumes there with mount, which runs
// mount.nfs4: the mount of a server that does not listen fails in mount.nfs4.
const longhornScript = `
set -u
fail() { echo "$*"; exit 1; }
pids=$(pidof iscsid) || fail "iscsid does not run"
for p in $pids; do
  for ns in mnt net; do
    [ "$(readlink /proc/$p/ns/$ns)" = "$(readlink /proc/1/ns/$ns)" ] || fail "iscsid ($p) has a $ns namespace of its own"
  done
  nsenter -t $p -m -n iscsiadm --version || fail "iscsiadm does not run in iscsid's namespaces"
  nsenter -t $p -m -n iscsiadm -m iface >/dev/null || fail "iscsiadm -m iface fails in iscsid's namespaces"
done
grep -qx 'InitiatorName=iqn.2026-11.dev.chalkos:[0-9a-f]\{32\}' /proc/1/root/etc/iscsi/initiatorname.iscsi \
  || fail "the initiator name is $(cat /proc/1/root/etc/iscsi/initiatorname.iscsi 2>&1)"
if out=$(nsenter -t $p -m -n iscsiadm -m discovery -t sendtargets -p 127.0.0.1:3260 2>&1); then
  fail "the discovery of 127.0.0.1 found targets: $out"
fi
case "$out" in *"cannot make connection to 127.0.0.1"*) ;; *) fail "iscsiadm's discovery did not reach iscsid: $out" ;; esac
for tool in fstrim cryptsetup dmsetup; do
  for dir in /usr/bin /usr/sbin; do
    nsenter -t 1 -m $dir/$tool --version >/dev/null || fail "$dir/$tool does not run in the host's mount namespace"
  done
  nsenter -t 1 -m $tool --version >/dev/null || fail "$tool is not on the PATH in the host's mount namespace"
done
mkdir -p /proc/1/root/run/chalktest-nfs
if out=$(nsenter -t 1 -m -n -u mount -t nfs4 -o retry=0 127.0.0.1:/chalktest /run/chalktest-nfs 2>&1); then
  fail "mount mounted 127.0.0.1:/chalktest"
fi
case "$out" in *"mount.nfs4: "*) ;; *) fail "mount did not run mount.nfs4: $out" ;; esac
`

// longhornTools checks w1's Longhorn host integration.
func longhornTools(t *testing.T, ctx context.Context, cs kubernetes.Interface) {
	t.Helper()
	hostPod(t, ctx, cs, "longhorn-tools", longhornScript, nil)
}

// modulePolicyTampered has w1's host switch IPE to permissive and back, and waits for chalkctl
// status to report the policy as tampered. It runs last: w1 stays tampered until it reboots.
func modulePolicyTampered(t *testing.T, ctx context.Context, cs kubernetes.Interface, nodes map[string]*node) {
	t.Helper()
	hostPod(t, ctx, cs, "module-policy-toggle", "touch /drop/toggle", map[string]string{"/drop": "/var/lib/chalktest-modules"})
	waitFor(t, 3*time.Minute, "w1's status to report the module policy as tampered", func() error {
		out, err := chalkctl(t, nodes["w1"], "base", "status", "w1")
		if err != nil {
			return err
		}
		if !strings.Contains(out, "module policy: tampered: IPE was switched to permissive") {
			return fmt.Errorf("status of w1:\n%s", out)
		}
		return nil
	})
}
