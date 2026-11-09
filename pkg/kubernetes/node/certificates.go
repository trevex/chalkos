package node

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/internal/poll"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// LockPKI waits for the lock on the control plane's certificates until ctx ends. A node leaving
// etcd holds it while it removes the static pods, so no renewal renders them again. The wait
// polls, as flock cannot be interrupted: a preparation waiting for the node's addresses may hold
// the lock for long.
func (s State) LockPKI(ctx context.Context) (unlock func(), err error) {
	err = poll.UntilDone(ctx, "the lock on the control plane's certificates", 100*time.Millisecond, func() error {
		var err error
		unlock, err = s.lockPKI(false)
		if err != nil && !errors.Is(err, errPKIBusy) {
			return poll.Permanent{Err: err}
		}
		return err
	})
	return unlock, err
}

// errPKIBusy means another process writes the control plane's certificates.
var errPKIBusy = errors.New("the node's Kubernetes files are being prepared")

// lockPKI takes the lock on the control plane's certificates; with wait unset it fails with
// errPKIBusy while another holds it. The lock ends with the process, so a crash leaves none.
func (s State) lockPKI(wait bool) (unlock func(), err error) {
	if err := os.MkdirAll(s.RunDir, 0o755); err != nil {
		return nil, err
	}
	f, err := atomicfile.Lock(s.pkiLock(), 0o600, wait)
	if errors.Is(err, atomicfile.ErrLocked) {
		return nil, errPKIBusy
	}
	if err != nil {
		return nil, err
	}
	return func() { f.Close() }, nil
}

// replaceDir replaces dir with the files in one step. It writes them, readable by root only
// because they hold keys, to a sibling directory and exchanges that with dir.
//
// Static pods mount dir by path: a container started afterwards sees the new files, and one still
// running keeps the old directory. The old directory stays as OldPKI until the next replacement.
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
func (s State) ControlPlaneRenewAt() (time.Time, *x509.Certificate, error) {
	files, err := readDir(s.PKI)
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
	return pki.RenewAt(first), first, nil
}

// RenewControlPlane issues the control plane's certificates and kubeconfigs again, for the
// addresses the preparation picked, and replaces them at once; a bootstrapped node renders its
// static pods again, whose certificate hash makes the kubelet start each one again. It waits
// until ctx ends for a preparation that runs, which writes them itself, and leaves the current
// ones on any failure.
//
// Static pods that still run on the certificates before the current ones, as after a renewal
// interrupted before it rendered them, are only rendered: issuing again would remove the
// certificates they run on.
func (s State) RenewControlPlane(ctx context.Context, now time.Time) error {
	unlock, err := s.LockPKI(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.CheckPrepared(); err != nil {
		return err
	}
	renders, err := s.rendersStaticPods()
	if err != nil {
		return err
	}
	in, err := s.podInputs()
	if err != nil {
		return err
	}
	if renders {
		if wrote, err := s.refreshFrom(in); err != nil || wrote {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if in.share, err = s.ReadShare(); err != nil {
		return err
	}
	if err := in.check(); err != nil {
		return err
	}
	files, err := kpki.ControlPlane(in.share, in.cluster, in.node, now)
	if err != nil {
		return err
	}
	if err := replaceDir(s.PKI, files); err != nil {
		return err
	}
	if !renders {
		return nil
	}
	if err := s.CheckEtcdData(); err != nil {
		return err
	}
	// The static pods are rendered from the certificates as they are now in PKI.
	return s.renderFrom(in, "")
}
