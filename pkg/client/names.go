package client

// The names chalkd's status gives the certificates and the trusted values it reports, in
// CertificateStatus.name and TrustStatus.name. Clients find an entry by these names; each
// layer names its own certificates.
const (
	// NameNode is the node's own certificate, which chalkd serves with.
	NameNode = "node"
	// NameOSCA is a CA of the OS, which issues the node certificates and client certificates.
	NameOSCA = "OS CA"
	// NameNodeCA is the CA control planes renew node certificates with.
	NameNodeCA = "node CA"
)
