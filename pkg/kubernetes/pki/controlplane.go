package pki

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/pki"
)

// Names the control plane's certificates carry.
const (
	APIServerKubeletClientUser = "chalkos:kube-apiserver-kubelet-client"
	APIServerEtcdClientUser    = "chalkos:kube-apiserver-etcd-client"
	FrontProxyClientUser       = "front-proxy-client"
	ControllerManagerUser      = "system:kube-controller-manager"
	SchedulerUser              = "system:kube-scheduler"
	// ChalkdUser is chalkd's own identity on the control plane, held only in memory. It is in
	// system:masters because it creates the RBAC bindings everything else depends on.
	ChalkdUser   = "chalkos:chalkd"
	MastersGroup = "system:masters"
)

// Files of the control plane's certificates, keys and kubeconfigs, by path relative to the
// directory the static pods mount.
// The CA files ca.crt, front-proxy-ca.crt and etcd/ca.crt hold every CA the components trust,
// the one that issues first; ca-signing.crt holds the issuing Kubernetes CA alone, with which the
// controller-manager signs, and sa.pub every service-account public key the API server accepts.
const (
	FileCA                      = "ca.crt"
	FileCASigning               = "ca-signing.crt"
	FileCAKey                   = "ca.key"
	FileFrontProxyCA            = "front-proxy-ca.crt"
	FileFrontProxyClient        = "front-proxy-client.crt"
	FileFrontProxyClientKey     = "front-proxy-client.key"
	FileAPIServer               = "apiserver.crt"
	FileAPIServerKey            = "apiserver.key"
	FileAPIServerKubeletClient  = "apiserver-kubelet-client.crt"
	FileAPIServerKubeletKey     = "apiserver-kubelet-client.key"
	FileAPIServerEtcdClient     = "apiserver-etcd-client.crt"
	FileAPIServerEtcdClientKey  = "apiserver-etcd-client.key"
	FileServiceAccountKey       = "sa.key"
	FileServiceAccountPub       = "sa.pub"
	FileEncryptionConfig        = "encryption.json"
	FileAuthenticationConfig    = "authentication.json"
	FileControllerManagerConfig = "controller-manager.kubeconfig"
	FileSchedulerConfig         = "scheduler.kubeconfig"
	FileEtcdCA                  = "etcd/ca.crt"
	FileEtcdServer              = "etcd/server.crt"
	FileEtcdServerKey           = "etcd/server.key"
	FileEtcdPeer                = "etcd/peer.crt"
	FileEtcdPeerKey             = "etcd/peer.key"
)

// ControlPlane issues the certificates and kubeconfigs of a control-plane node's static pods
// from its share: one year each, issued again at every boot.
func ControlPlane(s Share, c kubernetes.Cluster, n kubernetes.Node, now time.Time) (map[string][]byte, error) {
	if s.Kind != kubernetes.KindControlPlane {
		return nil, errors.New("the node's share is not a control-plane share")
	}
	host := c.EndpointHost()
	loopback := []netip.Addr{netip.AddrFrom4([4]byte{127, 0, 0, 1}), netip.IPv6Loopback()}
	nodeIPs := n.CertificateIPs()

	apiServerIPs := append(append([]netip.Addr{c.APIServerServiceIP()}, loopback...), nodeIPs...)
	apiServerNames := []string{"localhost", "kubernetes", "kubernetes.default", "kubernetes.default.svc", "kubernetes.default.svc." + c.Domain, n.Name}
	if ip, err := netip.ParseAddr(host); err == nil && ip.Zone() == "" {
		apiServerIPs = append(apiServerIPs, ip.Unmap())
	} else {
		apiServerNames = append(apiServerNames, host)
	}
	// Clients may reach the API server through any VIP, not only the endpoint's.
	for _, vip := range c.VIPAddresses() {
		if !slices.Contains(apiServerIPs, vip) {
			apiServerIPs = append(apiServerIPs, vip)
		}
	}
	etcdIPs := append(slices.Clone(loopback), nodeIPs...)
	etcdNames := []string{"localhost", n.Name}

	files := map[string][]byte{
		FileCA:           []byte(s.CABundle()),
		FileCASigning:    []byte(s.CA.Certificate),
		FileCAKey:        []byte(s.CA.Key),
		FileFrontProxyCA: []byte(s.FrontProxyCABundle()),
		FileEtcdCA:       []byte(s.EtcdCABundle()),
	}
	leaves := []struct {
		cert, key string
		ca        pki.CertKey
		leaf      pki.Leaf
	}{
		{FileAPIServer, FileAPIServerKey, s.CA, pki.Leaf{CommonName: "kube-apiserver", DNSNames: apiServerNames, IPs: certificateIPs(apiServerIPs), Server: true}},
		{FileAPIServerKubeletClient, FileAPIServerKubeletKey, s.CA, pki.Leaf{CommonName: APIServerKubeletClientUser, Client: true}},
		{FileAPIServerEtcdClient, FileAPIServerEtcdClientKey, *s.EtcdCA, pki.Leaf{CommonName: APIServerEtcdClientUser, Client: true}},
		{FileFrontProxyClient, FileFrontProxyClientKey, *s.FrontProxyCA, pki.Leaf{CommonName: FrontProxyClientUser, Client: true}},
		{FileEtcdServer, FileEtcdServerKey, *s.EtcdCA, pki.Leaf{CommonName: n.Name, DNSNames: etcdNames, IPs: certificateIPs(etcdIPs), Server: true, Client: true}},
		{FileEtcdPeer, FileEtcdPeerKey, *s.EtcdCA, pki.Leaf{CommonName: n.Name, DNSNames: etcdNames, IPs: certificateIPs(etcdIPs), Server: true, Client: true}},
	}
	for _, l := range leaves {
		ck, err := pki.IssueLeaf(l.ca, l.leaf, now)
		if err != nil {
			return nil, fmt.Errorf("issue %s: %w", l.cert, err)
		}
		files[l.cert] = []byte(ck.Certificate)
		files[l.key] = []byte(ck.Key)
	}

	for _, kc := range []struct{ file, user string }{
		{FileControllerManagerConfig, ControllerManagerUser},
		{FileSchedulerConfig, SchedulerUser},
	} {
		ck, err := pki.IssueLeaf(s.CA, pki.Leaf{CommonName: kc.user, Client: true}, now)
		if err != nil {
			return nil, fmt.Errorf("issue %s: %w", kc.file, err)
		}
		data, err := Kubeconfig{Name: "chalkos", Server: c.LocalAPIServer(), CA: []byte(s.CABundle()), Client: ck}.Encode()
		if err != nil {
			return nil, err
		}
		files[kc.file] = data
	}

	pubs, err := s.ServiceAccountPublicKeys()
	if err != nil {
		return nil, fmt.Errorf("service account key: %w", err)
	}
	files[FileServiceAccountKey] = []byte(s.ServiceAccountKey)
	files[FileServiceAccountPub] = []byte(strings.Join(pubs, ""))

	encryption, err := encryptionConfig(s.EncryptionKeys())
	if err != nil {
		return nil, err
	}
	files[FileEncryptionConfig] = encryption
	files[FileAuthenticationConfig], err = authenticationConfig()
	if err != nil {
		return nil, err
	}
	return files, nil
}

// certificateIPs converts addresses to a certificate's IP addresses.
func certificateIPs(addrs []netip.Addr) []net.IP {
	ips := make([]net.IP, len(addrs))
	for i, a := range addrs {
		ips[i] = net.IP(a.AsSlice())
	}
	return ips
}

// authenticationConfiguration is the API server's authentication configuration file, its fields
// in key order.
type authenticationConfiguration struct {
	Anonymous struct {
		Conditions []anonymousCondition `json:"conditions"`
		Enabled    bool                 `json:"enabled"`
	} `json:"anonymous"`
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
}

type anonymousCondition struct {
	Path string `json:"path"`
}

// authenticationConfig allows anonymous requests only to the health endpoints, which the
// kubelet probes without credentials.
func authenticationConfig() ([]byte, error) {
	config := authenticationConfiguration{APIVersion: "apiserver.config.k8s.io/v1", Kind: "AuthenticationConfiguration"}
	config.Anonymous.Enabled = true
	for _, path := range []string{"/livez", "/readyz", "/healthz"} {
		config.Anonymous.Conditions = append(config.Anonymous.Conditions, anonymousCondition{Path: path})
	}
	return json.MarshalIndent(config, "", "  ")
}

// EncryptedResources are the resources the API server encrypts in etcd.
var EncryptedResources = []string{"secrets"}

// The API server's encryption configuration file, its fields in key order.
type (
	encryptionConfiguration struct {
		APIVersion string                `json:"apiVersion"`
		Kind       string                `json:"kind"`
		Resources  []encryptionResources `json:"resources"`
	}
	encryptionResources struct {
		Providers []encryptionProvider `json:"providers"`
		Resources []string             `json:"resources"`
	}
	// encryptionProvider holds one provider.
	encryptionProvider struct {
		Identity  *struct{}  `json:"identity,omitempty"`
		Secretbox *secretbox `json:"secretbox,omitempty"`
	}
	secretbox struct {
		Keys []secretboxKey `json:"keys"`
	}
	secretboxKey struct {
		Name   string `json:"name"`
		Secret []byte `json:"secret"`
	}
)

// encryptionConfig encrypts secrets with secretbox and the first of the keys; the others decrypt
// what was encrypted with them. identity still reads secrets written before encryption was
// configured.
func encryptionConfig(keys []pki.EncryptionKey) ([]byte, error) {
	var box secretbox
	for _, key := range keys {
		if len(key.Key) != pki.EncryptionKeySize {
			return nil, fmt.Errorf("the encryption key %s must be %d bytes", key.Name, pki.EncryptionKeySize)
		}
		box.Keys = append(box.Keys, secretboxKey{Name: key.Name, Secret: key.Key})
	}
	return json.MarshalIndent(encryptionConfiguration{
		APIVersion: "apiserver.config.k8s.io/v1",
		Kind:       "EncryptionConfiguration",
		Resources: []encryptionResources{{
			Resources: EncryptedResources,
			Providers: []encryptionProvider{{Secretbox: &box}, {Identity: &struct{}{}}},
		}},
	}, "", "  ")
}

// ParseEncryptionConfig reads the secretbox keys of an encryption configuration encryptionConfig
// wrote, the encrypting one first.
func ParseEncryptionConfig(data []byte) ([]pki.EncryptionKey, error) {
	var config encryptionConfiguration
	if err := json.Unmarshal(data, &config); err != nil {
		// The configuration holds keys: the error does not quote it.
		return nil, errors.New("the encryption configuration is no JSON")
	}
	for _, r := range config.Resources {
		for _, p := range r.Providers {
			if p.Secretbox == nil {
				continue
			}
			var keys []pki.EncryptionKey
			for _, k := range p.Secretbox.Keys {
				keys = append(keys, pki.EncryptionKey{Name: k.Name, Key: k.Secret})
			}
			return keys, nil
		}
	}
	return nil, errors.New("the encryption configuration has no secretbox keys")
}

// IssueChalkd issues chalkd's client certificate on a control-plane node, valid for validity.
func IssueChalkd(s Share, validity time.Duration, now time.Time) (pki.CertKey, error) {
	if s.Kind != kubernetes.KindControlPlane {
		return pki.CertKey{}, errors.New("only control-plane nodes hold the CA key")
	}
	if validity <= 0 {
		return pki.CertKey{}, errors.New("chalkd's certificate needs a validity")
	}
	return pki.IssueLeaf(s.CA, pki.Leaf{CommonName: ChalkdUser, Organization: []string{MastersGroup}, Client: true, Validity: validity}, now)
}

// IssueEtcdClient issues chalkd's client certificate for etcd from the share's etcd CA, valid
// for validity. etcd accepts any client certificate of its CA.
func IssueEtcdClient(s Share, validity time.Duration, now time.Time) (pki.CertKey, error) {
	if s.Kind != kubernetes.KindControlPlane || s.EtcdCA == nil {
		return pki.CertKey{}, errors.New("only control-plane nodes hold the etcd CA key")
	}
	if validity <= 0 {
		return pki.CertKey{}, errors.New("chalkd's certificate needs a validity")
	}
	return pki.IssueLeaf(*s.EtcdCA, pki.Leaf{CommonName: ChalkdUser, Client: true, Validity: validity}, now)
}

// Leaves are the files of the control plane's leaf certificates, renewed together, and
// Kubeconfigs those of the kubeconfigs that embed one.
var (
	Leaves      = []string{FileAPIServer, FileAPIServerKubeletClient, FileAPIServerEtcdClient, FileFrontProxyClient, FileEtcdServer, FileEtcdPeer}
	Kubeconfigs = []string{FileControllerManagerConfig, FileSchedulerConfig}
)

// ControlPlaneLeaves parses the leaf certificates of a control plane's files, as ControlPlane
// issues them, by file name.
func ControlPlaneLeaves(files map[string][]byte) (map[string]*x509.Certificate, error) {
	leaves := map[string]*x509.Certificate{}
	for _, name := range Leaves {
		cert, err := pki.ParseCertificate(files[name])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		leaves[name] = cert
	}
	for _, name := range Kubeconfigs {
		cert, err := kubeconfigCertificate(files[name])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		leaves[name] = cert
	}
	return leaves, nil
}

// kubeconfigCertificate parses the client certificate a kubeconfig embeds.
func kubeconfigCertificate(data []byte) (*x509.Certificate, error) {
	var kc kubeconfigFile
	if err := json.Unmarshal(data, &kc); err != nil {
		return nil, fmt.Errorf("parse the kubeconfig: %w", err)
	}
	if len(kc.Users) != 1 {
		return nil, errors.New("the kubeconfig has no single user")
	}
	return pki.ParseCertificate(kc.Users[0].User.ClientCertificateData)
}
