package kubernetes

// Directories and files of Kubernetes on a node.
const (
	// RunDir holds the files chalkd and the preparation write at boot: the cluster, the kubelet's
	// files, the control plane's certificates and static pods, and the credential provider's files.
	RunDir = "/run/chalkos/kubernetes"
	// ClusterFile is the cluster chalkd builds from the node's config and image.
	ClusterFile = RunDir + "/cluster.json"
	// PKIDir holds the control plane's certificates, which its static pods mount.
	PKIDir = RunDir + "/pki"
	// CredentialProviderDir holds the kubelet's image credential provider's files. The Nix module
	// names the same directory.
	CredentialProviderDir = RunDir + "/credential-provider"
	// ImageFile is the image's Kubernetes facts; it exists exactly on images with Kubernetes.
	ImageFile = "/etc/chalkos/kubernetes/image.json"
	// EtcdDataDir is etcd's data directory on VAR.
	EtcdDataDir = "/var/lib/etcd"
)
