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

// State is the node's Kubernetes state in the files at its paths: the share, the markers of its
// etcd membership and preparation, its addresses and its static pods.
type State struct {
	Paths
}

// ErrNoShare means the node has no Kubernetes share yet.
var ErrNoShare = errors.New("the node has no Kubernetes share; deliver one with chalkctl install or chalkctl apply-config --kubernetes-share")

// ReadShare reads the node's share from STATE.
func (s State) ReadShare() (kpki.Share, error) {
	data, err := os.ReadFile(s.Share())
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

// EtcdInitialised reports whether etcd on this node answered ready after the bootstrap.
func (s State) EtcdInitialised() (bool, error) {
	return marked(s.EtcdInitialisedMarker())
}

// MarkJoining records that the node starts joining the cluster at the endpoint.
func (s State) MarkJoining(endpoint string) error {
	return atomicfile.WriteFile(s.JoiningMarker(), []byte(endpoint+"\n"), 0o644)
}

// MarkJoined records that the node joined the cluster: from then on it is a bootstrapped control
// plane that renders its static pods at boot. The bootstrapped marker comes first, so the node is
// marked as one or the other at any time.
func (s State) MarkJoined(now time.Time) error {
	if err := s.MarkBootstrapped(now); err != nil {
		return fmt.Errorf("record the join: %w", err)
	}
	return removeIfExists(s.JoiningMarker())
}

// MarkBootstrapped records that the node is a bootstrapped control plane, which renders its
// static pods at boot.
func (s State) MarkBootstrapped(now time.Time) error {
	return markAt(s.BootstrappedMarker(), now)
}

// ClearMembership removes what makes the node an etcd member at boot: the bootstrapped,
// etcd-initialised and joining markers, the initial cluster and, last, the pin. etcd's data stays.
// The caller records first that the node left etcd: a node interrupted here must not look like one
// that has yet to join.
func (s State) ClearMembership() error {
	for _, f := range []string{s.BootstrappedMarker(), s.EtcdInitialisedMarker(), s.EtcdInitialCluster(), s.JoiningMarker(), s.Pin()} {
		if err := removeIfExists(f); err != nil {
			return err
		}
	}
	return nil
}

// MarkLeft records that the node left etcd. It comes before the node deletes its etcd data and
// ClearMembership, so a node interrupted there never looks like one that has yet to join.
func (s State) MarkLeft(now time.Time) error {
	return markAt(s.LeftMarker(), now)
}

// RemoveEtcdData deletes etcd's data and keeps its directory.
func (s State) RemoveEtcdData() error {
	entries, err := os.ReadDir(s.EtcdData)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(s.EtcdData, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// EtcdHasData reports whether etcd's data directory holds anything.
func (s State) EtcdHasData() (bool, error) {
	entries, err := os.ReadDir(s.EtcdData)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) > 0, nil
}

// Prepared reports whether Prepare finished and succeeded since it last started.
func (s State) Prepared() (bool, error) {
	return marked(s.PreparedMarker())
}

// ErrNotPrepared means the node's Kubernetes files were not prepared since the preparation last
// started.
var ErrNotPrepared = errors.New("the node's Kubernetes files are not prepared yet")

// CheckPrepared returns ErrNotPrepared unless Prepare finished and succeeded since it last started.
func (s State) CheckPrepared() error {
	prepared, err := s.Prepared()
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
func (s State) PreparationError() (string, error) {
	data, err := os.ReadFile(s.PrepareError())
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// MarkEtcdInitialised records that etcd answered ready; it keeps an existing marker.
func (s State) MarkEtcdInitialised(now time.Time) error {
	initialised, err := s.EtcdInitialised()
	if err != nil || initialised {
		return err
	}
	return markAt(s.EtcdInitialisedMarker(), now)
}

// CheckEtcdData refuses an empty etcd data directory once etcd was initialised. Without the
// marker an empty directory is a bootstrap that was interrupted before etcd became ready, which
// may be retried.
func (s State) CheckEtcdData() error {
	initialised, err := s.EtcdInitialised()
	if err != nil || !initialised {
		return err
	}
	hasData, err := s.EtcdHasData()
	if err != nil {
		return err
	}
	if !hasData {
		return ErrEtcdDataMissing
	}
	return nil
}

// WriteInitialCluster records the initial cluster the node's etcd joins with.
func (s State) WriteInitialCluster(initialCluster string) error {
	return atomicfile.WriteFile(s.EtcdInitialCluster(), []byte(initialCluster+"\n"), 0o644)
}

// readInitialCluster returns the initial cluster the node joined with, "" when it started its own.
func (s State) readInitialCluster() (string, error) {
	data, err := os.ReadFile(s.EtcdInitialCluster())
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	initial := strings.TrimSpace(string(data))
	if initial == "" {
		return "", fmt.Errorf("%s is empty", s.EtcdInitialCluster())
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
