package node

import (
	"path/filepath"

	"github.com/trevex/chalkos/pkg/kubernetes"
)

// Paths are the files Prepare reads and writes; tests point them at temporary directories.
type Paths struct {
	// State is the Kubernetes directory on STATE, holding the share and the bootstrap and etcd
	// markers.
	State string
	// Cluster is the cluster chalkd builds from the node's config and image.
	Cluster string
	// Image is the image's Kubernetes facts; it exists exactly on images with Kubernetes.
	Image string
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
		Cluster:    kubernetes.ClusterFile,
		Image:      kubernetes.ImageFile,
		NodeFile:   "/run/chalkos/node.json",
		Run:        kubernetes.RunDir,
		PKI:        kubernetes.PKIDir,
		KubeletPKI: "/var/lib/kubelet/pki",
		EtcdData:   kubernetes.EtcdDataDir,
	}
}

func (p Paths) Share() string        { return filepath.Join(p.State, "share.json") }
func (p Paths) Bootstrapped() string { return filepath.Join(p.State, "bootstrapped") }
func (p Paths) Manifests() string    { return filepath.Join(p.Run, "manifests") }
func (p Paths) KubeletDir() string   { return filepath.Join(p.Run, "kubelet") }

// NodeIP holds the addresses the node picked, one per line, the primary family's first.
func (p Paths) NodeIP() string { return filepath.Join(p.Run, "node-ip") }

// VXLAN holds what the firewall's VXLAN rule accepts: a line "destination <address> <mode>" for
// each of the node's addresses, where mode is interface or any, and a line "source <range>" for
// each range VXLAN must come from.
func (p Paths) VXLAN() string { return filepath.Join(p.Run, "vxlan") }

// Pin holds the addresses a control-plane node was pinned to when it became an etcd member: its
// etcd peers and the certificates know it by them, so every later boot uses exactly these.
func (p Paths) Pin() string { return filepath.Join(p.State, "node-ip") }

// PrepareError holds why the last preparation failed, such as that no address of the node
// matched.
func (p Paths) PrepareError() string { return filepath.Join(p.Run, "prepare.error") }

// Prepared marks that Prepare finished: chalkd starts before it at boot and must not read the
// files it is still writing.
func (p Paths) Prepared() string { return filepath.Join(p.Run, "prepared") }

// Left marks that the node left etcd: it joins the cluster again only once reinstalled.
func (p Paths) Left() string { return filepath.Join(p.State, "left") }

// EtcdInitialCluster holds etcd's initial cluster when the node joined an existing cluster.
func (p Paths) EtcdInitialCluster() string { return filepath.Join(p.State, "etcd-initial-cluster") }

// EtcdInitialised marks that etcd answered ready after the bootstrap, so its data exists.
func (p Paths) EtcdInitialised() string { return filepath.Join(p.State, "etcd-initialised") }

// Joining marks that the node started joining the cluster at the endpoint it holds: it may be an
// etcd member from then on, so it never bootstraps a cluster of its own.
func (p Paths) Joining() string { return filepath.Join(p.State, "joining") }

// Kubeconfig is the kubelet's kubeconfig, which chalkd also reads the Node with.
func (p Paths) Kubeconfig() string { return filepath.Join(p.KubeletDir(), "kubeconfig") }

// KubeletConfig is the kubelet's configuration file.
func (p Paths) KubeletConfig() string { return filepath.Join(p.KubeletDir(), "config.json") }

// KubeletClient is the file the kubelet's certificate store keeps its current client certificate
// in; it is a link to the newest one. KubeletServing is that of its serving certificate.
func (p Paths) KubeletClient() string {
	return filepath.Join(p.KubeletPKI, "kubelet-client-current.pem")
}

func (p Paths) KubeletServing() string {
	return filepath.Join(p.KubeletPKI, "kubelet-server-current.pem")
}

// OldPKI is where the certificates the static pods ran with before the last replacement stay
// until the next one: running containers keep the directory they mounted, and the kubelet
// starts each pod again with the new one.
func (p Paths) OldPKI() string { return p.PKI + ".old" }

// pkiLock serialises the writers of the control plane's certificates: the preparation, a
// separate process, and chalkd's renewal.
func (p Paths) pkiLock() string { return filepath.Join(p.Run, ".pki.lock") }
