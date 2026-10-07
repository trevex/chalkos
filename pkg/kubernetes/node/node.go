// Package node prepares a node's Kubernetes files at boot: the node's address, the kubelet's
// credentials, kubeconfig and flags from the node's share and identity, and on control-plane
// nodes the control plane's certificates and, once bootstrapped, its static pods.
package node

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/manifests"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// Paths are the files Prepare reads and writes; tests point them at temporary directories.
type Paths struct {
	// State is the Kubernetes directory on STATE, holding the share and the bootstrap and etcd
	// markers.
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

// NodeIP holds the address the node picked; the firewall's VXLAN rule reads it too.
func (p Paths) NodeIP() string { return filepath.Join(p.Run, "node-ip") }

// NodeIPError holds why the node has no address.
func (p Paths) NodeIPError() string { return filepath.Join(p.Run, "node-ip.error") }

// EtcdInitialised marks that etcd answered ready after the bootstrap, so its data exists.
func (p Paths) EtcdInitialised() string { return filepath.Join(p.State, "etcd-initialised") }

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

// MarkEtcdInitialised records that etcd answered ready; it keeps an existing marker.
func MarkEtcdInitialised(p Paths, now time.Time) error {
	initialised, err := EtcdInitialised(p)
	if err != nil || initialised {
		return err
	}
	return install.WriteFile(p.EtcdInitialised(), []byte(now.UTC().Format(time.RFC3339)+"\n"), 0o644)
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

// Resolver picks the node's address.
type Resolver func(c kubernetes.Cluster, n kubernetes.Node) (net.IP, error)

// ResolveNodeIP waits up to the cluster's timeout for the address the node's identity and its
// cluster select.
func ResolveNodeIP(c kubernetes.Cluster, n kubernetes.Node) (net.IP, error) {
	sel, err := c.NodeIPSelector(n)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(c.NodeIP.Timeout) * time.Second
	log.Printf("picking the node's address by %s, waiting up to %v", sel, timeout)
	ip, err := nodeip.NewWaiter().Wait(sel, timeout)
	if err != nil {
		return nil, err
	}
	log.Printf("the node's address is %s", ip)
	return net.IP(ip.AsSlice()), nil
}

// ReadNodeIP reads the address Prepare picked.
func ReadNodeIP(p Paths) (net.IP, error) {
	data, err := os.ReadFile(p.NodeIP())
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(strings.TrimSpace(string(data)))
	if ip == nil {
		return nil, fmt.Errorf("%s holds no address", p.NodeIP())
	}
	return ip, nil
}

// NodeIPProblem says why the node has no address, or returns "" once Prepare picked one.
func NodeIPProblem(p Paths) string {
	_, err := ReadNodeIP(p)
	switch {
	case err == nil:
		return ""
	case !errors.Is(err, fs.ErrNotExist):
		return err.Error()
	}
	if data, err := os.ReadFile(p.NodeIPError()); err == nil {
		return strings.TrimSpace(string(data))
	}
	return "waiting for the node's address"
}

// Prepare picks the node's address and writes the kubelet's files and, on a control-plane node,
// the control plane's certificates, and its static pods once the node is bootstrapped. A node
// without a share or an address gets none of them, so its kubelet does not start.
func Prepare(p Paths, now time.Time, resolve Resolver) error {
	// An address picked before must not outlive an attempt that fails.
	for _, f := range []string{p.NodeIP(), p.NodeIPError()} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	share, c, n, err := Load(p)
	if errors.Is(err, ErrNoShare) {
		log.Print(err)
		return os.RemoveAll(p.KubeletDir())
	}
	if err != nil {
		return err
	}
	if n.IP, err = resolve(c, n); err != nil {
		for _, dir := range []string{p.KubeletDir(), p.Manifests()} {
			if rerr := os.RemoveAll(dir); rerr != nil {
				log.Print(rerr)
			}
		}
		// chalkd reports the reason in the node's status.
		if werr := install.WriteFile(p.NodeIPError(), []byte(err.Error()+"\n"), 0o644); werr != nil {
			log.Print(werr)
		}
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
	if err := installKubeletClient(p, share.CA, *kubeletCert, n.Name, now); err != nil {
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
	// Written once the certificates naming it are, which Bootstrap relies on.
	if err := install.WriteFile(p.NodeIP(), []byte(n.IP.String()+"\n"), 0o644); err != nil {
		return err
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
// store holds one the kubelet renewed itself that is trusted and expires later.
func installKubeletClient(p Paths, ca pki.CertKey, ck pki.CertKey, node string, now time.Time) error {
	cert, _, err := ck.Parse()
	if err != nil {
		return fmt.Errorf("kubelet client certificate: %w", err)
	}
	switch err := checkRenewedClient(p.kubeletClient(), ca, cert, node, now); {
	case err == nil:
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		log.Printf("kubelet: installing the share's client certificate instead of %s: %v", p.kubeletClient(), err)
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

// checkRenewedClient returns nil if the kubelet's current client certificate in path should be
// kept: it was issued by ca for node with its own key, is valid now and expires after want.
// Errors never include the file's contents.
func checkRenewedClient(path string, ca pki.CertKey, want *x509.Certificate, node string, now time.Time) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// The kubelet stores the certificate and its key in one file; X509KeyPair also checks that
	// the key belongs to the certificate, whatever encoding the key has.
	pair, err := tls.X509KeyPair(data, data)
	if err != nil {
		return fmt.Errorf("not a certificate with its key: %w", err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	caCert, err := pki.ParseCertificate([]byte(ca.Certificate))
	if err != nil {
		return fmt.Errorf("the cluster CA: %w", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, CurrentTime: now}); err != nil {
		return err
	}
	if cert.Subject.CommonName != kpki.NodeUserPrefix+node || !slices.Equal(cert.Subject.Organization, []string{kpki.NodesGroup}) {
		return fmt.Errorf("the certificate is for %q in groups %q, want node %s in %q only", cert.Subject.CommonName, cert.Subject.Organization, node, kpki.NodesGroup)
	}
	if !cert.NotAfter.After(want.NotAfter) {
		return errors.New("it expires no later than the share's certificate")
	}
	return nil
}

// RenderStaticPods writes the static pods from the certificates Prepare wrote, unless etcd's
// data is missing.
func RenderStaticPods(p Paths) error {
	if err := CheckEtcdData(p); err != nil {
		return err
	}
	c, err := kubernetes.ReadCluster(p.Cluster)
	if err != nil {
		return err
	}
	n, err := kubernetes.ReadNode(p.NodeFile)
	if err != nil {
		return err
	}
	if n.IP, err = ReadNodeIP(p); err != nil {
		return fmt.Errorf("the node's address: %w", err)
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
