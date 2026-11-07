package pki

// The names chalkd's status gives the Kubernetes certificates and trusted values, in
// CertificateStatus.name and TrustStatus.name, beside the OS's in pkg/client.
const (
	// NameKubernetesCA is the cluster's CA.
	NameKubernetesCA = "Kubernetes CA"
	// NameFrontProxyCA is the CA of the API server's front proxy.
	NameFrontProxyCA = "front-proxy CA"
	// NameEtcdCA is etcd's CA.
	NameEtcdCA = "etcd CA"
	// NameControlPlane is the control plane's leaf certificates, by the one that expires first.
	NameControlPlane = "Kubernetes control plane"
	// NameKubeletClient is the kubelet's client certificate.
	NameKubeletClient = "kubelet client"
	// NameKubeletServing is the kubelet's serving certificate.
	NameKubeletServing = "kubelet serving"
	// NameKubernetesShare stands for the certificates of a share the node cannot read.
	NameKubernetesShare = "Kubernetes share"
	// NameServiceAccountKeys is the keys service-account tokens are signed with.
	NameServiceAccountKeys = "service-account keys"
	// NameEncryptionKeys is the keys the API server encrypts secrets with.
	NameEncryptionKeys = "encryption keys"
)
