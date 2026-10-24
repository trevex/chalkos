package node

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/atomicfile"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

// ErrNoShare means the node has no Kubernetes share yet.
var ErrNoShare = errors.New("the node has no Kubernetes share; deliver one with chalkctl install or chalkctl apply-config --kubernetes-share")

// ReadShare reads the node's share from STATE.
func ReadShare(p Paths) (kpki.Share, error) {
	data, err := os.ReadFile(p.Share())
	if errors.Is(err, fs.ErrNotExist) {
		return kpki.Share{}, ErrNoShare
	}
	if err != nil {
		return kpki.Share{}, err
	}
	return kpki.ParseShare(data)
}

// ErrEtcdDataMissing means etcd's data directory is empty on a node that initialised etcd, as
// after VAR was reset: etcd would start a new, empty cluster.
var ErrEtcdDataMissing = errors.New("etcd data is missing on a node whose cluster was initialised; restore etcd or reinstall the node")

// Bootstrapped reports whether this control-plane node was told to initialise etcd.
func Bootstrapped(p Paths) (bool, error) {
	return marked(p.Bootstrapped())
}

// EtcdInitialised reports whether etcd on this node answered ready after the bootstrap.
func EtcdInitialised(p Paths) (bool, error) {
	return marked(p.EtcdInitialised())
}

// Joining reports whether the node started joining the cluster and has not finished.
func Joining(p Paths) (bool, error) {
	return marked(p.Joining())
}

// MarkJoining records that the node starts joining the cluster at the endpoint.
func MarkJoining(p Paths, endpoint string) error {
	return atomicfile.WriteFile(p.Joining(), []byte(endpoint+"\n"), 0o644)
}

// MarkJoined records that the node joined the cluster: from then on it is a bootstrapped control
// plane that renders its static pods at boot. The bootstrapped marker comes first, so the node is
// marked as one or the other at any time.
func MarkJoined(p Paths, now time.Time) error {
	if err := MarkBootstrapped(p, now); err != nil {
		return fmt.Errorf("record the join: %w", err)
	}
	return removeIfExists(p.Joining())
}

// MarkBootstrapped records that the node is a bootstrapped control plane, which renders its
// static pods at boot.
func MarkBootstrapped(p Paths, now time.Time) error {
	return markAt(p.Bootstrapped(), now)
}

// ClearMembership removes what makes the node an etcd member at boot: the bootstrapped,
// etcd-initialised and joining markers, the initial cluster and, last, the pin. etcd's data stays.
// The caller records first that the node left etcd: a node interrupted here must not look like one
// that has yet to join.
func ClearMembership(p Paths) error {
	for _, f := range []string{p.Bootstrapped(), p.EtcdInitialised(), p.EtcdInitialCluster(), p.Joining(), p.Pin()} {
		if err := removeIfExists(f); err != nil {
			return err
		}
	}
	return nil
}

// Left reports whether the node left etcd.
func Left(p Paths) (bool, error) {
	return marked(p.Left())
}

// MarkLeft records that the node left etcd. It comes before the node deletes its etcd data and
// ClearMembership, so a node interrupted there never looks like one that has yet to join.
func MarkLeft(p Paths, now time.Time) error {
	return markAt(p.Left(), now)
}

// RemoveEtcdData deletes etcd's data and keeps its directory.
func RemoveEtcdData(p Paths) error {
	entries, err := os.ReadDir(p.EtcdData)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(p.EtcdData, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// EtcdHasData reports whether etcd's data directory holds anything.
func EtcdHasData(p Paths) (bool, error) {
	entries, err := os.ReadDir(p.EtcdData)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) > 0, nil
}

// Prepared reports whether Prepare finished and succeeded since it last started.
func Prepared(p Paths) (bool, error) {
	return marked(p.Prepared())
}

// ErrNotPrepared means the node's Kubernetes files were not prepared since the preparation last
// started.
var ErrNotPrepared = errors.New("the node's Kubernetes files are not prepared yet")

// CheckPrepared returns ErrNotPrepared unless Prepare finished and succeeded since it last started.
func CheckPrepared(p Paths) error {
	prepared, err := Prepared(p)
	if err != nil {
		return err
	}
	if !prepared {
		return ErrNotPrepared
	}
	return nil
}

// PreparationError reads why the last preparation failed; "" when none failed since the last one
// started.
func PreparationError(p Paths) (string, error) {
	data, err := os.ReadFile(p.PrepareError())
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// MarkEtcdInitialised records that etcd answered ready; it keeps an existing marker.
func MarkEtcdInitialised(p Paths, now time.Time) error {
	initialised, err := EtcdInitialised(p)
	if err != nil || initialised {
		return err
	}
	return markAt(p.EtcdInitialised(), now)
}

// CheckEtcdData refuses an empty etcd data directory once etcd was initialised. Without the
// marker an empty directory is a bootstrap that was interrupted before etcd became ready, which
// may be retried.
func CheckEtcdData(p Paths) error {
	initialised, err := EtcdInitialised(p)
	if err != nil || !initialised {
		return err
	}
	hasData, err := EtcdHasData(p)
	if err != nil {
		return err
	}
	if !hasData {
		return ErrEtcdDataMissing
	}
	return nil
}

// WriteInitialCluster records the initial cluster the node's etcd joins with.
func WriteInitialCluster(p Paths, initialCluster string) error {
	return atomicfile.WriteFile(p.EtcdInitialCluster(), []byte(initialCluster+"\n"), 0o644)
}

// HasInitialCluster reports whether the node recorded the initial cluster its etcd joins with.
func HasInitialCluster(p Paths) (bool, error) {
	return marked(p.EtcdInitialCluster())
}

// readInitialCluster returns the initial cluster the node joined with, "" when it started its own.
func readInitialCluster(p Paths) (string, error) {
	data, err := os.ReadFile(p.EtcdInitialCluster())
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	initial := strings.TrimSpace(string(data))
	if initial == "" {
		return "", fmt.Errorf("%s is empty", p.EtcdInitialCluster())
	}
	return initial, nil
}

// marked reports whether a marker file exists. Only a marker that does not exist is unset.
func marked(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, err
}

// markAt writes the marker at path, holding the time it was set.
func markAt(path string, now time.Time) error {
	return atomicfile.WriteFile(path, []byte(now.UTC().Format(time.RFC3339)+"\n"), 0o644)
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
