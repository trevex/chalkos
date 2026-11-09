package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/kubernetes/manifests"
)

// RenderStaticPods writes the static pods from the certificates Prepare wrote, unless etcd's
// data is missing.
func (s State) RenderStaticPods() error {
	return s.render("")
}

// RenderEtcd writes etcd's static pod alone, for a node whose etcd joins as a learner: its API
// server would not start before etcd is a voter.
func (s State) RenderEtcd() error {
	return s.render("etcd.json")
}

// RemoveStaticPods removes the static pods, so the kubelet stops the control plane.
func (s State) RemoveStaticPods() error {
	entries, err := os.ReadDir(s.Manifests())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.Remove(filepath.Join(s.Manifests(), e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// RefreshStaticPods renders the static pods again when they run on other certificates than the
// current ones, as chalkd's control-plane loop does when it starts. Manifests that would not
// change are not written, so no pod starts again. A preparation that runs renders them itself.
func (s State) RefreshStaticPods() error {
	unlock, err := s.lockPKI(false)
	if errors.Is(err, errPKIBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unlock()
	if prepared, err := s.Prepared(); err != nil || !prepared {
		return err
	}
	if renders, err := s.rendersStaticPods(); err != nil || !renders {
		return err
	}
	pods, err := s.currentPods()
	if err != nil {
		return err
	}
	if stale, err := s.stale(pods); err != nil || !stale {
		return err
	}
	if err := s.CheckEtcdData(); err != nil {
		return err
	}
	return s.writePods(pods, "")
}

// StaticPodsStale reports whether a static pod of a node that renders them names other
// certificates, by their hash, than the control plane's current ones.
func (s State) StaticPodsStale() (bool, error) {
	if renders, err := s.rendersStaticPods(); err != nil || !renders {
		return false, err
	}
	pods, err := s.currentPods()
	if err != nil {
		return false, err
	}
	return s.stale(pods)
}

// stale reports whether a static pod written names other certificates, by their hash, than its
// manifest in pods.
func (s State) stale(pods map[string][]byte) (bool, error) {
	for name, data := range pods {
		rendered, err := os.ReadFile(filepath.Join(s.Manifests(), name))
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
func (s State) rendersStaticPods() (bool, error) {
	m, _, err := s.ReadMembership()
	return m == MembershipBootstrapped, err
}

// certificatesHash returns the hash of the certificates a static pod's manifest names.
func certificatesHash(manifest []byte) (string, error) {
	var pod corev1.Pod
	if err := json.Unmarshal(manifest, &pod); err != nil {
		return "", err
	}
	return pod.Annotations[manifests.CertificatesAnnotation], nil
}

// render writes the static pods, or only the one in the file only names.
func (s State) render(only string) error {
	if err := s.CheckEtcdData(); err != nil {
		return err
	}
	pods, err := s.currentPods()
	if err != nil {
		return err
	}
	return s.writePods(pods, only)
}

// renderFrom writes the static pods from in and the certificates in PKI, unless etcd's data is
// missing.
func (s State) renderFrom(in inputs) error {
	if err := s.CheckEtcdData(); err != nil {
		return err
	}
	files, err := s.readPKI()
	if err != nil {
		return err
	}
	pods, err := s.renderPods(in, files)
	if err != nil {
		return err
	}
	return s.writePods(pods, "")
}

// writePods writes the static pods, or only the one in the file only names.
func (s State) writePods(pods map[string][]byte, only string) error {
	if err := os.MkdirAll(s.Manifests(), 0o755); err != nil {
		return err
	}
	for name, data := range pods {
		if only != "" && name != only {
			continue
		}
		if err := atomicfile.WriteFile(filepath.Join(s.Manifests(), name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// currentPods renders the static pods from the files Prepare wrote.
func (s State) currentPods() (map[string][]byte, error) {
	in, err := s.podInputs()
	if err != nil {
		return nil, err
	}
	files, err := s.readPKI()
	if err != nil {
		return nil, err
	}
	return s.renderPods(in, files)
}

// podInputs reads the cluster file and the node's identity with the addresses Prepare picked,
// which the static pods are rendered from besides the certificates.
func (s State) podInputs() (inputs, error) {
	in, err := s.readNode()
	if err != nil {
		return inputs{}, err
	}
	if in.node.IPs, err = s.ReadNodeIPs(); err != nil {
		return inputs{}, fmt.Errorf("the node's addresses: %w", err)
	}
	return in, nil
}

// readPKI reads the control plane's certificates, by their path in PKI.
func (s State) readPKI() (map[string][]byte, error) {
	files, err := readDir(s.PKI)
	if err != nil {
		return nil, fmt.Errorf("read the control plane's certificates: %w", err)
	}
	return files, nil
}

// renderPods renders the static pods from in and the control plane's certificates, by file name.
func (s State) renderPods(in inputs, files map[string][]byte) (map[string][]byte, error) {
	initialCluster, err := s.readInitialCluster()
	if err != nil {
		return nil, err
	}
	return manifests.StaticPods(in.cluster, in.node, s.PKI, files, initialCluster)
}

func readDir(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[rel] = data
		return nil
	})
	return files, err
}
