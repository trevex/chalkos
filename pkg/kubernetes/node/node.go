// Package node prepares a node's Kubernetes files at boot: the kubelet's credentials,
// kubeconfig and flags from the node's share and identity, and on control-plane nodes the
// control plane's certificates and, once bootstrapped, its static pods.
package node

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/manifests"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// Paths are the files Prepare reads and writes; tests point them at temporary directories.
type Paths struct {
	// State is the Kubernetes directory on STATE, holding the share and the bootstrap marker.
	State string
	// Cluster is the image's cluster file.
	Cluster string
	// NodeFile is the node's identity without secrets.
	NodeFile string
	// Run receives the kubelet's files, the certificates and the static pods.
	Run string
	// PKI is where the static pods find the certificates.
	PKI string
	// KubeletPKI is the kubelet's certificate directory on VAR.
	KubeletPKI string
	// EtcdData is etcd's data directory on VAR.
	EtcdData string
}

// DefaultPaths are the paths on a node.
func DefaultPaths() Paths {
	return Paths{
		State:      "/state/kubernetes",
		Cluster:    "/etc/chalkos/kubernetes/cluster.json",
		NodeFile:   "/run/chalkos/node.json",
		Run:        "/run/chalkos/kubernetes",
		PKI:        manifests.PKIDir,
		KubeletPKI: "/var/lib/kubelet/pki",
		EtcdData:   manifests.EtcdDataDir,
	}
}

func (p Paths) Share() string        { return filepath.Join(p.State, "share.json") }
func (p Paths) Bootstrapped() string { return filepath.Join(p.State, "bootstrapped") }
func (p Paths) Manifests() string    { return filepath.Join(p.Run, "manifests") }
func (p Paths) KubeletDir() string   { return filepath.Join(p.Run, "kubelet") }

// Kubeconfig is the kubelet's kubeconfig, which chalkd also reads the Node with.
func (p Paths) Kubeconfig() string { return filepath.Join(p.KubeletDir(), "kubeconfig") }

// kubeletClient is the file the kubelet's certificate store keeps its current client
// certificate in; it is a link to the newest one.
func (p Paths) kubeletClient() string {
	return filepath.Join(p.KubeletPKI, "kubelet-client-current.pem")
}

// ErrNoShare means the node has no Kubernetes share yet.
var ErrNoShare = errors.New("the node has no Kubernetes share; deliver one with chalkctl install or chalkctl apply-identity --kubernetes-share")

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

// Bootstrapped reports whether this control-plane node initialised etcd. Only a marker that
// does not exist means not bootstrapped.
func Bootstrapped(p Paths) (bool, error) {
	_, err := os.Stat(p.Bootstrapped())
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, err
}

// Load reads the node's share, its image's cluster file and its identity, and checks they
// belong together.
func Load(p Paths) (kpki.Share, kubernetes.Cluster, kubernetes.Node, error) {
	share, err := ReadShare(p)
	if err != nil {
		return kpki.Share{}, kubernetes.Cluster{}, kubernetes.Node{}, err
	}
	c, err := kubernetes.ReadCluster(p.Cluster)
	if err != nil {
		return kpki.Share{}, kubernetes.Cluster{}, kubernetes.Node{}, err
	}
	n, err := kubernetes.ReadNode(p.NodeFile)
	if err != nil {
		return kpki.Share{}, kubernetes.Cluster{}, kubernetes.Node{}, err
	}
	if share.Kind != c.Kind {
		return kpki.Share{}, kubernetes.Cluster{}, kubernetes.Node{}, fmt.Errorf("the node's share is for a %s node, but its image is for %s nodes", share.Kind, c.Kind)
	}
	if share.Kind == kubernetes.KindWorker && share.Node() != n.Name {
		return kpki.Share{}, kubernetes.Cluster{}, kubernetes.Node{}, fmt.Errorf("the node's kubelet certificate is for node %s, not %s", share.Node(), n.Name)
	}
	return share, c, n, nil
}

// Prepare writes the kubelet's files and, on a control-plane node, the control plane's
// certificates, and its static pods once the node is bootstrapped. A node without a share gets
// nothing, so its kubelet does not start.
func Prepare(p Paths, now time.Time) error {
	share, c, n, err := Load(p)
	if errors.Is(err, ErrNoShare) {
		log.Print(err)
		return os.RemoveAll(p.KubeletDir())
	}
	if err != nil {
		return err
	}
	kubeletCert := share.Kubelet
	if c.Kind == kubernetes.KindControlPlane {
		files, err := kpki.ControlPlane(share, c, n, now)
		if err != nil {
			return err
		}
		if err := writeDir(p.PKI, files); err != nil {
			return err
		}
		issued, err := kpki.IssueKubeletClient(share.CA, n.Name, now)
		if err != nil {
			return err
		}
		kubeletCert = &issued
	}
	if err := installKubeletClient(p, *kubeletCert); err != nil {
		return err
	}
	kubeconfig, err := kpki.Kubeconfig{
		Name:       "chalkos",
		Server:     c.Endpoint,
		CAFile:     filepath.Join(p.KubeletDir(), "ca.crt"),
		ClientFile: p.kubeletClient(),
	}.Encode()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.KubeletDir(), 0o755); err != nil {
		return err
	}
	for name, data := range map[string][]byte{
		"ca.crt": []byte(share.CA.Certificate),
		"flags":  []byte("KUBELET_ARGS=" + strings.Join(KubeletFlags(c, n), " ") + "\n"),
	} {
		if err := install.WriteFile(filepath.Join(p.KubeletDir(), name), data, 0o644); err != nil {
			return err
		}
	}
	// The kubelet starts once its kubeconfig exists, so it is written last.
	if err := install.WriteFile(p.Kubeconfig(), kubeconfig, 0o644); err != nil {
		return err
	}
	if c.Kind != kubernetes.KindControlPlane {
		return nil
	}
	if err := os.MkdirAll(p.Manifests(), 0o755); err != nil {
		return err
	}
	bootstrapped, err := Bootstrapped(p)
	if err != nil || !bootstrapped {
		return err
	}
	return RenderStaticPods(p)
}

// KubeletFlags are the kubelet's node-specific flags: its name, address, labels and taints.
// Control-plane nodes are tainted unless the cluster allows workloads on them.
func KubeletFlags(c kubernetes.Cluster, n kubernetes.Node) []string {
	flags := []string{"--hostname-override=" + n.Name}
	if n.IP != nil {
		flags = append(flags, "--node-ip="+n.IP.String())
	}
	var labels []string
	for k, v := range n.Labels {
		labels = append(labels, k+"="+v)
	}
	sort.Strings(labels)
	if len(labels) > 0 {
		flags = append(flags, "--node-labels="+strings.Join(labels, ","))
	}
	var taints []string
	if c.Kind == kubernetes.KindControlPlane && !c.AllowSchedulingOnControlPlanes {
		taints = append(taints, "node-role.kubernetes.io/control-plane:NoSchedule")
	}
	for _, t := range n.Taints {
		taint := t.Key
		if t.Value != "" {
			taint += "=" + t.Value
		}
		taints = append(taints, taint+":"+t.Effect)
	}
	if len(taints) > 0 {
		flags = append(flags, "--register-with-taints="+strings.Join(taints, ","))
	}
	return flags
}

// installKubeletClient puts the certificate into the kubelet's certificate store unless the
// store holds one that expires later, which the kubelet renewed itself.
func installKubeletClient(p Paths, ck pki.CertKey) error {
	cert, _, err := ck.Parse()
	if err != nil {
		return fmt.Errorf("kubelet client certificate: %w", err)
	}
	// The store writes the certificate before the key, so the first PEM block is the certificate.
	if current, err := os.ReadFile(p.kubeletClient()); err == nil {
		if have, err := pki.ParseCertificate(current); err == nil && have.NotAfter.After(cert.NotAfter) {
			return nil
		}
	}
	if err := os.MkdirAll(p.KubeletPKI, 0o700); err != nil {
		return err
	}
	// One file, replaced atomically; the kubelet writes renewed certificates next to it.
	name := "kubelet-client-chalkos.pem"
	if err := install.WriteFile(filepath.Join(p.KubeletPKI, name), []byte(ck.Certificate+ck.Key), 0o600); err != nil {
		return err
	}
	// The kubelet's store follows the link and refuses to replace a file that is not one.
	tmp := p.kubeletClient() + ".chalkos"
	os.Remove(tmp)
	if err := os.Symlink(name, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, p.kubeletClient())
}

// RenderStaticPods writes the static pods from the certificates Prepare wrote.
func RenderStaticPods(p Paths) error {
	c, err := kubernetes.ReadCluster(p.Cluster)
	if err != nil {
		return err
	}
	n, err := kubernetes.ReadNode(p.NodeFile)
	if err != nil {
		return err
	}
	files, err := readDir(p.PKI)
	if err != nil {
		return fmt.Errorf("read the control plane's certificates: %w", err)
	}
	pods, err := manifests.StaticPods(c, n, files)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.Manifests(), 0o755); err != nil {
		return err
	}
	for name, data := range pods {
		if err := install.WriteFile(filepath.Join(p.Manifests(), name), data, 0o644); err != nil {
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

// writeDir replaces dir with the files, readable by root only, as they hold keys.
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
