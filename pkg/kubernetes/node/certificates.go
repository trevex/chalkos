package node

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/kubernetes/manifests"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

// OldPKI is where the certificates the static pods ran with before the last replacement stay
// until the next one: running containers keep the directory they mounted, and the kubelet
// starts each pod again with the new one.
func (p Paths) OldPKI() string { return p.PKI + ".old" }

// pkiLock serialises the writers of the control plane's certificates: the preparation, a
// separate process, and chalkd's renewal.
func (p Paths) pkiLock() string { return filepath.Join(p.Run, ".pki.lock") }

// ErrNotPrepared means the node's Kubernetes files were not prepared since the preparation last
// started.
var ErrNotPrepared = errors.New("the node's Kubernetes files are not prepared")

// LockPKI waits for the lock on the control plane's certificates until ctx ends. A node leaving
// etcd holds it while it removes the static pods, so no renewal renders them again. The wait
// polls, as flock cannot be interrupted: a preparation waiting for the node's addresses may hold
// the lock for long.
func LockPKI(ctx context.Context, p Paths) (unlock func(), err error) {
	for {
		unlock, err := lockPKI(p, false)
		if !errors.Is(err, ErrPKIBusy) {
			return unlock, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for the lock on the control plane's certificates, which a preparation holds: %w", context.Cause(ctx))
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// ErrPKIBusy means another process writes the control plane's certificates.
var ErrPKIBusy = errors.New("the node's Kubernetes files are being prepared")

// lockPKI takes the lock on the control plane's certificates; with wait unset it fails with
// ErrPKIBusy while another holds it. The lock ends with the process, so a crash leaves none.
func lockPKI(p Paths, wait bool) (unlock func(), err error) {
	if err := os.MkdirAll(p.Run, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p.pkiLock(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrPKIBusy
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}

// replaceDir replaces dir with the files, readable by root only, as they hold keys, in one step:
// the files are written to a sibling directory, which is exchanged with dir. Static pods mount
// dir by path, so a container started afterwards sees the new files, and one still running keeps
// the old directory, which stays as OldPKI until the next replacement.
func replaceDir(dir string, files map[string][]byte) error {
	next, old := dir+".new", dir+".old"
	if err := writeDir(next, files); err != nil {
		return err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, next, unix.AT_FDCWD, dir, unix.RENAME_EXCHANGE); errors.Is(err, unix.ENOENT) {
		return os.Rename(next, dir)
	} else if err != nil {
		return fmt.Errorf("replace %s: %w", dir, err)
	}
	if err := os.RemoveAll(old); err != nil {
		return err
	}
	return os.Rename(next, old)
}

// writeDir writes the files to dir, which must not exist yet but for an earlier attempt.
func writeDir(dir string, files map[string][]byte) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for name, data := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := install.WriteFile(path, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// ControlPlaneLeaves reads the leaf certificates of the control plane's files by file name.
func ControlPlaneLeaves(p Paths) (map[string]*x509.Certificate, error) {
	files, err := readDir(p.PKI)
	if err != nil {
		return nil, err
	}
	return kpki.ControlPlaneLeaves(files)
}

// ControlPlaneRenewAt is when the control plane's certificates are renewed: once two thirds of
// the lifetime of the one that expires first have passed. It returns that certificate too.
func ControlPlaneRenewAt(p Paths) (time.Time, *x509.Certificate, error) {
	leaves, err := ControlPlaneLeaves(p)
	if err != nil {
		return time.Time{}, nil, err
	}
	var first *x509.Certificate
	for _, cert := range leaves {
		if first == nil || cert.NotAfter.Before(first.NotAfter) {
			first = cert
		}
	}
	return first.NotBefore.Add(first.NotAfter.Sub(first.NotBefore) * 2 / 3), first, nil
}

// RenewControlPlane issues the control plane's certificates and kubeconfigs again, for the
// addresses the preparation picked, and replaces them at once; a bootstrapped node renders its
// static pods again, whose certificate hash makes the kubelet start each one again. It fails
// with ErrPKIBusy while a preparation runs, which writes them itself, and leaves the current
// ones on any failure.
//
// Static pods that still run on the certificates before the current ones, as after a renewal
// interrupted before it rendered them, are only rendered: issuing again would remove the
// certificates they run on.
func RenewControlPlane(ctx context.Context, p Paths, now time.Time) error {
	unlock, err := lockPKI(p, false)
	if err != nil {
		return err
	}
	defer unlock()
	if prepared, err := Prepared(p); err != nil {
		return err
	} else if !prepared {
		return ErrNotPrepared
	}
	if stale, err := StaticPodsStale(p); err != nil {
		return err
	} else if stale {
		return RenderStaticPods(p)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	share, c, n, err := Load(p)
	if err != nil {
		return err
	}
	if n.IPs, err = ReadNodeIPs(p); err != nil {
		return err
	}
	files, err := kpki.ControlPlane(share, c, n, now)
	if err != nil {
		return err
	}
	if err := replaceDir(p.PKI, files); err != nil {
		return err
	}
	if renders, err := rendersStaticPods(p); err != nil || !renders {
		return err
	}
	return RenderStaticPods(p)
}

// RefreshStaticPods renders the static pods again when they run on other certificates than the
// current ones, as chalkd's control-plane loop does when it starts. Manifests that would not
// change are not written, so no pod starts again. A preparation that runs renders them itself.
func RefreshStaticPods(p Paths) error {
	unlock, err := lockPKI(p, false)
	if errors.Is(err, ErrPKIBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unlock()
	if prepared, err := Prepared(p); err != nil || !prepared {
		return err
	}
	if stale, err := StaticPodsStale(p); err != nil || !stale {
		return err
	}
	return RenderStaticPods(p)
}

// StaticPodsStale reports whether a static pod of a node that renders them names other
// certificates, by their hash, than the control plane's current ones.
func StaticPodsStale(p Paths) (bool, error) {
	if renders, err := rendersStaticPods(p); err != nil || !renders {
		return false, err
	}
	pods, err := renderPods(p)
	if err != nil {
		return false, err
	}
	for name, data := range pods {
		rendered, err := os.ReadFile(filepath.Join(p.Manifests(), name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		want, err := certificatesHash(data)
		if err != nil {
			return false, err
		}
		got, err := certificatesHash(rendered)
		if err != nil {
			return false, fmt.Errorf("%s: %w", name, err)
		}
		if got != want {
			return true, nil
		}
	}
	return false, nil
}

// rendersStaticPods reports whether the node runs the control plane's static pods: once
// bootstrapped, until it left etcd.
func rendersStaticPods(p Paths) (bool, error) {
	if bootstrapped, err := Bootstrapped(p); err != nil || !bootstrapped {
		return false, err
	}
	left, err := Left(p)
	return !left, err
}

// certificatesHash returns the hash of the certificates a static pod's manifest names.
func certificatesHash(manifest []byte) (string, error) {
	var pod struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(manifest, &pod); err != nil {
		return "", err
	}
	return pod.Metadata.Annotations[manifests.CertificatesAnnotation], nil
}
