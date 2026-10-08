package node

import (
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
func bootstrappedControlPlane(t *testing.T) Paths {
	t.Helper()
	p := testNode(t, kubernetes.KindControlPlane, "cp1", secrets(t))
	write(t, p.Bootstrapped(), "")
	if err := Prepare(p, time.Now().Add(-time.Hour), picked, nil); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRenewControlPlane(t *testing.T) {
	p := bootstrappedControlPlane(t)
	before := readFile(t, filepath.Join(p.PKI, kpki.FileAPIServer))
	pod := readFile(t, filepath.Join(p.Manifests(), "kube-apiserver.json"))
	renewAt, first, err := ControlPlaneRenewAt(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := first.NotBefore.Add(first.NotAfter.Sub(first.NotBefore) * 2 / 3); !renewAt.Equal(want) || first.NotAfter.Sub(first.NotBefore) < pki.LeafValidity {
		t.Errorf("renewal at %v for a certificate of %v to %v", renewAt, first.NotBefore, first.NotAfter)
	}

	if err := RenewControlPlane(p, time.Now()); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, filepath.Join(p.PKI, kpki.FileAPIServer))
	if after == before {
		t.Fatal("the API server's certificate was not issued again")
	}
	if kept := readFile(t, filepath.Join(p.OldPKI(), kpki.FileAPIServer)); kept != before {
		t.Error("the certificates the running pods mounted are gone")
	}
	leaves, err := ControlPlaneLeaves(p)
	if err != nil {
		t.Fatal(err)
	}
	for name, cert := range leaves {
		if !cert.NotBefore.After(first.NotBefore) {
			t.Errorf("%s was not issued again", name)
		}
	}
	// The kubelet starts the pod again because its certificate hash changed.
	if readFile(t, filepath.Join(p.Manifests(), "kube-apiserver.json")) == pod {
		t.Error("the API server's static pod did not change")
	}
	// The pods name the same addresses as before.
	ips, err := ReadNodeIPs(p)
	if err != nil || len(ips) != 1 || ips[0].String() != "192.168.100.11" {
		t.Errorf("node addresses %v, %v", ips, err)
	}
	cert := leaves[kpki.FileAPIServer]
	if err := cert.VerifyHostname("192.168.100.11"); err != nil {
		t.Error(err)
	}
}

func TestRenewControlPlaneWaitsForThePreparation(t *testing.T) {
	p := bootstrappedControlPlane(t)
	before := readFile(t, filepath.Join(p.PKI, kpki.FileAPIServer))
	unlock, err := lockPKI(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := RenewControlPlane(p, time.Now()); !errors.Is(err, ErrPKIBusy) {
		t.Errorf("while a preparation runs: %v, want ErrPKIBusy", err)
	}
	unlock()
	if err := os.Remove(p.Prepared()); err != nil {
		t.Fatal(err)
	}
	if err := RenewControlPlane(p, time.Now()); !errors.Is(err, ErrPKIBusy) {
		t.Errorf("before the preparation finished: %v, want ErrPKIBusy", err)
	}
	if readFile(t, filepath.Join(p.PKI, kpki.FileAPIServer)) != before {
		t.Error("a refused renewal changed the certificates")
	}
}

func TestPrepareFailureRemovesOldCertificates(t *testing.T) {
	p := bootstrappedControlPlane(t)
	if err := RenewControlPlane(p, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(p, time.Now(), func(sel nodeip.Selector, _ time.Duration) ([]nodeip.Address, error) {
		return nil, errors.New("no address")
	}, nil); err == nil {
		t.Fatal("prepared without an address")
	}
	for _, dir := range []string{p.PKI, p.OldPKI()} {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s remained after a failed preparation: %v", dir, err)
		}
	}
}
