package node

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// inMountNamespace reports whether the test runs in its own user and mount namespace, where it may
// bind-mount directories as the container runtime does. Called outside one, it runs the test
// again in a new one and returns false.
func inMountNamespace(t *testing.T) bool {
	t.Helper()
	if os.Getenv("CHALKOS_TEST_MOUNTNS") == "1" {
		return true
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), "CHALKOS_TEST_MOUNTNS=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	out, err := cmd.CombinedOutput()
	t.Logf("in a new mount namespace:\n%s", out)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return false
}

// TestReplaceDirUnderMounts replaces a directory a container mounted, as the static pods mount
// the control plane's certificates: the running container keeps what it mounted, one started
// afterwards sees the new files.
func TestReplaceDirUnderMounts(t *testing.T) {
	if !inMountNamespace(t) {
		return
	}
	root := t.TempDir()
	dir, running, started := filepath.Join(root, "pki"), filepath.Join(root, "running"), filepath.Join(root, "started")
	if err := replaceDir(dir, map[string][]byte{"a.crt": []byte("first"), "etcd/b.crt": []byte("first")}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{running, started} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The runtime binds the host path into the container.
	if err := unix.Mount(dir, running, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Unmount(running, unix.MNT_DETACH) })
	if err := replaceDir(dir, map[string][]byte{"a.crt": []byte("second"), "etcd/b.crt": []byte("second")}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(running, "etcd/b.crt")); got != "first" {
		t.Errorf("the running container sees %q, want the files it started with", got)
	}
	if err := unix.Mount(dir, started, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Unmount(started, unix.MNT_DETACH) })
	if got := readFile(t, filepath.Join(started, "etcd/b.crt")); got != "second" {
		t.Errorf("a container started after the replacement sees %q, want the new files", got)
	}
	if got := readFile(t, filepath.Join(dir+".old", "a.crt")); got != "first" {
		t.Errorf("the replaced files are not kept: %q", got)
	}
	if _, err := os.Stat(dir + ".new"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the sibling directory remained: %v", err)
	}
	// The next replacement drops the files from before the last one.
	if err := replaceDir(dir, map[string][]byte{"a.crt": []byte("third")}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir+".old", "a.crt")); got != "second" {
		t.Errorf("kept %q, want the files of the last replacement", got)
	}
	for _, d := range []string{dir, dir + ".old", filepath.Join(dir+".old", "etcd")} {
		if info, err := os.Stat(d); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v, %v; want mode 0700", d, info, err)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "a.crt")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("a.crt: %v, %v; want mode 0600", info, err)
	}
}

// bootstrappedControlPlane prepares a bootstrapped control-plane node with its static pods.
func bootstrappedControlPlane(t *testing.T) State {
	t.Helper()
	s := testNode(t, kubernetes.KindControlPlane, "cp1", secrets(t))
	write(t, s.BootstrappedMarker(), "")
	if err := s.Prepare(time.Now().Add(-time.Hour), picked, nil); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRenewControlPlane(t *testing.T) {
	s := bootstrappedControlPlane(t)
	before := readFile(t, filepath.Join(s.PKI, kpki.FileAPIServer))
	pod := readFile(t, filepath.Join(s.Manifests(), "kube-apiserver.json"))
	renewAt, first, err := s.ControlPlaneRenewAt()
	if err != nil {
		t.Fatal(err)
	}
	if want := first.NotBefore.Add(first.NotAfter.Sub(first.NotBefore) * 2 / 3); !renewAt.Equal(want) || first.NotAfter.Sub(first.NotBefore) < pki.LeafValidity {
		t.Errorf("renewal at %v for a certificate of %v to %v", renewAt, first.NotBefore, first.NotAfter)
	}

	if err := s.RenewControlPlane(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, filepath.Join(s.PKI, kpki.FileAPIServer))
	if after == before {
		t.Fatal("the API server's certificate was not issued again")
	}
	if kept := readFile(t, filepath.Join(s.OldPKI(), kpki.FileAPIServer)); kept != before {
		t.Error("the certificates the running pods mounted are gone")
	}
	files, err := readDir(s.PKI)
	if err != nil {
		t.Fatal(err)
	}
	leaves, err := kpki.ControlPlaneLeaves(files)
	if err != nil {
		t.Fatal(err)
	}
	for name, cert := range leaves {
		if !cert.NotBefore.After(first.NotBefore) {
			t.Errorf("%s was not issued again", name)
		}
	}
	// The kubelet starts the pod again because its certificate hash changed.
	if readFile(t, filepath.Join(s.Manifests(), "kube-apiserver.json")) == pod {
		t.Error("the API server's static pod did not change")
	}
	// The pods name the same addresses as before.
	ips, err := s.ReadNodeIPs()
	if err != nil || len(ips) != 1 || ips[0].String() != "192.168.100.11" {
		t.Errorf("node addresses %v, %v", ips, err)
	}
	cert := leaves[kpki.FileAPIServer]
	if err := cert.VerifyHostname("192.168.100.11"); err != nil {
		t.Error(err)
	}
}

func TestRenewControlPlaneWaitsForThePreparation(t *testing.T) {
	s := bootstrappedControlPlane(t)
	before := readFile(t, filepath.Join(s.PKI, kpki.FileAPIServer))
	unlock, err := s.lockPKI(true)
	if err != nil {
		t.Fatal(err)
	}
	// A renewal whose time ends while the preparation runs leaves the certificates.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := s.RenewControlPlane(ctx, time.Now()); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("while a preparation runs: %v, want the context's error", err)
	}
	if readFile(t, filepath.Join(s.PKI, kpki.FileAPIServer)) != before {
		t.Error("a renewal that did not get the lock changed the certificates")
	}
	// One that started while the preparation runs renews once it is done.
	result := make(chan error, 1)
	go func() { result <- s.RenewControlPlane(context.Background(), time.Now()) }()
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-result:
		t.Fatalf("the renewal ended while a preparation runs: %v", err)
	default:
	}
	unlock()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("after the preparation: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the renewal did not take the released lock")
	}
	if readFile(t, filepath.Join(s.PKI, kpki.FileAPIServer)) == before {
		t.Error("the renewal did not renew the certificates")
	}
	renewed := readFile(t, filepath.Join(s.PKI, kpki.FileAPIServer))
	if err := os.Remove(s.PreparedMarker()); err != nil {
		t.Fatal(err)
	}
	// Status shows the error: a node whose preparation failed is not being prepared.
	if err := s.RenewControlPlane(context.Background(), time.Now()); !errors.Is(err, ErrNotPrepared) {
		t.Errorf("before the preparation finished: %v, want ErrNotPrepared", err)
	}
	if readFile(t, filepath.Join(s.PKI, kpki.FileAPIServer)) != renewed {
		t.Error("a refused renewal changed the certificates")
	}
}

func TestPrepareFailureRemovesOldCertificates(t *testing.T) {
	s := bootstrappedControlPlane(t)
	if err := s.RenewControlPlane(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	// A preparation that failed while it wrote the certificates left their sibling directory.
	write(t, filepath.Join(s.PKI+".new", kpki.FileAPIServer), "partial")
	if err := s.Prepare(time.Now(), func(sel nodeip.Selector, _ time.Duration) ([]nodeip.Address, error) {
		return nil, errors.New("no address")
	}, nil); err == nil {
		t.Fatal("prepared without an address")
	}
	for _, dir := range []string{s.PKI, s.OldPKI(), s.PKI + ".new"} {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s remained after a failed preparation: %v", dir, err)
		}
	}
}

// certificatesAnnotation returns the certificate hash of the static pod in the file.
func certificatesAnnotation(t *testing.T, s State, file string) string {
	t.Helper()
	hash, err := certificatesHash([]byte(readFile(t, filepath.Join(s.Manifests(), file))))
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

// A renewal interrupted between replacing the certificates and rendering the static pods leaves
// the pods on the certificates before; the next renewal renders them for the current ones, and
// issues none, as the pods still run on the previous set it would remove.
func TestRenewControlPlaneRendersAfterAnInterruptedSwap(t *testing.T) {
	s := bootstrappedControlPlane(t)
	pod := certificatesAnnotation(t, s, "kube-apiserver.json")
	in, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	if in.node.IPs, err = s.ReadNodeIPs(); err != nil {
		t.Fatal(err)
	}
	files, err := kpki.ControlPlane(in.share, in.cluster, in.node, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceDir(s.PKI, files); err != nil {
		t.Fatal(err)
	}
	if stale, err := s.StaticPodsStale(); err != nil || !stale {
		t.Fatalf("stale = %v, %v after a swap without rendering", stale, err)
	}
	swapped := readFile(t, filepath.Join(s.PKI, kpki.FileAPIServer))
	if err := s.RenewControlPlane(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if certificatesAnnotation(t, s, "kube-apiserver.json") == pod {
		t.Error("the static pods still run on the replaced certificates")
	}
	if readFile(t, filepath.Join(s.PKI, kpki.FileAPIServer)) != swapped {
		t.Error("the certificates were issued again while the pods ran on the previous set")
	}
	if stale, err := s.StaticPodsStale(); err != nil || stale {
		t.Errorf("stale = %v, %v after rendering", stale, err)
	}

	// The control-plane loop renders them as it starts; unchanged, the manifests stay as they are.
	if files, err = kpki.ControlPlane(in.share, in.cluster, in.node, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := replaceDir(s.PKI, files); err != nil {
		t.Fatal(err)
	}
	before := certificatesAnnotation(t, s, "kube-apiserver.json")
	if err := s.RefreshStaticPods(); err != nil {
		t.Fatal(err)
	}
	refreshed := certificatesAnnotation(t, s, "kube-apiserver.json")
	if refreshed == before {
		t.Error("RefreshStaticPods left the pods on the replaced certificates")
	}
	info, err := os.Stat(filepath.Join(s.Manifests(), "kube-apiserver.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshStaticPods(); err != nil {
		t.Fatal(err)
	}
	if again, err := os.Stat(filepath.Join(s.Manifests(), "kube-apiserver.json")); err != nil || !again.ModTime().Equal(info.ModTime()) || !os.SameFile(info, again) {
		t.Error("RefreshStaticPods wrote manifests that had not changed")
	}
}

// A node that left etcd renders no static pods, whatever its certificates.
func TestStaticPodsNotStaleOnceLeft(t *testing.T) {
	s := bootstrappedControlPlane(t)
	if err := s.MarkLeft(time.Now()); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(s.PKI, kpki.FileAPIServer), "replaced")
	if stale, err := s.StaticPodsStale(); err != nil || stale {
		t.Errorf("stale = %v, %v on a node that left", stale, err)
	}
}

// Waiting for the lock ends with the context, so a node leaving etcd is not held up for good by
// a preparation that waits for its addresses.
func TestLockPKIEndsWithItsContext(t *testing.T) {
	s := bootstrappedControlPlane(t)
	unlock, err := s.lockPKI(true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		unlock, err := s.LockPKI(ctx)
		if err == nil {
			unlock()
		}
		result <- err
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want the context's", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waiting for the lock did not end with its context")
	}
	unlock()
	// Once released, the lock is taken.
	again, err := s.LockPKI(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	again()
}
