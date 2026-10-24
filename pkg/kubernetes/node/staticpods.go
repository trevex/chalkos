package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/manifests"
)

// RenderStaticPods writes the static pods from the certificates Prepare wrote, unless etcd's
// data is missing.
func RenderStaticPods(p Paths) error {
	return render(p, "")
}

// RenderEtcd writes etcd's static pod alone, for a node whose etcd joins as a learner: its API
// server would not start before etcd is a voter.
func RenderEtcd(p Paths) error {
	return render(p, "etcd.json")
}

// RemoveStaticPods removes the static pods, so the kubelet stops the control plane.
func RemoveStaticPods(p Paths) error {
	entries, err := os.ReadDir(p.Manifests())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.Remove(filepath.Join(p.Manifests(), e.Name())); err != nil {
			return err
		}
	}
	return nil
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
	m, _, err := ReadMembership(p)
	return m == MembershipBootstrapped, err
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

// render writes the static pods, or only the one in the file only names.
func render(p Paths, only string) error {
	if err := CheckEtcdData(p); err != nil {
		return err
	}
	pods, err := renderPods(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.Manifests(), 0o755); err != nil {
		return err
	}
	for name, data := range pods {
		if only != "" && name != only {
			continue
		}
		if err := atomicfile.WriteFile(filepath.Join(p.Manifests(), name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// renderPods renders the static pods from the certificates in PKI, by file name.
func renderPods(p Paths) (map[string][]byte, error) {
	c, err := kubernetes.ReadCluster(p.Cluster)
	if err != nil {
		return nil, err
	}
	n, err := kubernetes.ReadNode(p.NodeFile)
	if err != nil {
		return nil, err
	}
	if n.IPs, err = ReadNodeIPs(p); err != nil {
		return nil, fmt.Errorf("the node's addresses: %w", err)
	}
	files, err := readDir(p.PKI)
	if err != nil {
		return nil, fmt.Errorf("read the control plane's certificates: %w", err)
	}
	initialCluster, err := readInitialCluster(p)
	if err != nil {
		return nil, err
	}
	return manifests.StaticPods(c, n, p.PKI, files, initialCluster)
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
