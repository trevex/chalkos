package node

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/trevex/chalkos/pkg/atomicfile"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

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
		if err := atomicfile.WriteFile(path, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// ControlPlaneRenewAt is when the control plane's certificates are renewed: once two thirds of
// the lifetime of the one that expires first have passed. It returns that certificate too.
func ControlPlaneRenewAt(p Paths) (time.Time, *x509.Certificate, error) {
	files, err := readDir(p.PKI)
	if err != nil {
		return time.Time{}, nil, err
	}
	leaves, err := kpki.ControlPlaneLeaves(files)
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
	if err := CheckPrepared(p); err != nil {
		return err
	}
	if stale, err := StaticPodsStale(p); err != nil {
		return err
	} else if stale {
		return RenderStaticPods(p)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	share, c, n, err := load(p)
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
