package client

// The names chalkd's status gives the certificates and the trusted values it reports, in
// CertificateStatus.name and TrustStatus.name. Clients find an entry by these names.
const (
	// NameNode is the node's own certificate, which chalkd serves with.
	NameNode = "node"
	// NameOSCA is a CA of the OS, which issues the node certificates and client certificates.
	NameOSCA = "OS CA"
	// NameNodeCA is the CA control planes renew node certificates with.
	NameNodeCA = "node CA"
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
