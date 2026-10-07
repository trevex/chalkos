package pki

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
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

// LocalAPIServer is where components on a control-plane node reach their own API server.
const LocalAPIServer = "https://127.0.0.1:6443"

// Files of the control plane's certificates, keys and kubeconfigs, by path relative to the
// directory the static pods mount.
const (
	FileCA                      = "ca.crt"
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
	host, err := c.EndpointHost()
	if err != nil {
		return nil, err
	}
	serviceIP, err := c.APIServerServiceIP()
	if err != nil {
		return nil, err
	}
	loopback := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	nodeIPs := n.IPs()

	apiServerIPs := append(append([]net.IP{serviceIP}, loopback...), nodeIPs...)
	apiServerNames := []string{"localhost", "kubernetes", "kubernetes.default", "kubernetes.default.svc", "kubernetes.default.svc." + c.Domain, n.Name}
	if ip := net.ParseIP(host); ip != nil {
		apiServerIPs = append(apiServerIPs, ip)
	} else {
		apiServerNames = append(apiServerNames, host)
	}
	etcdIPs := append(append([]net.IP{}, loopback...), nodeIPs...)
	etcdNames := []string{"localhost", n.Name}

	files := map[string][]byte{
		FileCA:           []byte(s.CA.Certificate),
		FileCAKey:        []byte(s.CA.Key),
		FileFrontProxyCA: []byte(s.FrontProxyCA.Certificate),
		FileEtcdCA:       []byte(s.EtcdCA.Certificate),
	}
	leaves := []struct {
		cert, key string
		ca        pki.CertKey
		leaf      pki.Leaf
	}{
		{FileAPIServer, FileAPIServerKey, s.CA, pki.Leaf{CommonName: "kube-apiserver", DNSNames: apiServerNames, IPs: apiServerIPs, Server: true}},
		{FileAPIServerKubeletClient, FileAPIServerKubeletKey, s.CA, pki.Leaf{CommonName: APIServerKubeletClientUser, Client: true}},
		{FileAPIServerEtcdClient, FileAPIServerEtcdClientKey, *s.EtcdCA, pki.Leaf{CommonName: APIServerEtcdClientUser, Client: true}},
		{FileFrontProxyClient, FileFrontProxyClientKey, *s.FrontProxyCA, pki.Leaf{CommonName: FrontProxyClientUser, Client: true}},
		{FileEtcdServer, FileEtcdServerKey, *s.EtcdCA, pki.Leaf{CommonName: n.Name, DNSNames: etcdNames, IPs: etcdIPs, Server: true, Client: true}},
		{FileEtcdPeer, FileEtcdPeerKey, *s.EtcdCA, pki.Leaf{CommonName: n.Name, DNSNames: etcdNames, IPs: etcdIPs, Server: true, Client: true}},
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
		data, err := Kubeconfig{Name: "chalkos", Server: LocalAPIServer, CA: []byte(s.CA.Certificate), Client: ck}.Encode()
		if err != nil {
			return nil, err
		}
		files[kc.file] = data
	}

	key, err := pki.ParseECKey(s.ServiceAccountKey)
	if err != nil {
		return nil, fmt.Errorf("service account key: %w", err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	files[FileServiceAccountKey] = []byte(s.ServiceAccountKey)
	files[FileServiceAccountPub] = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})

	encryption, err := encryptionConfig(s.EncryptionKey)
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

// authenticationConfig allows anonymous requests only to the health endpoints, which the
// kubelet probes without credentials.
func authenticationConfig() ([]byte, error) {
	var conditions []any
	for _, path := range []string{"/livez", "/readyz", "/healthz"} {
		conditions = append(conditions, map[string]any{"path": path})
	}
	return json.MarshalIndent(map[string]any{
		"apiVersion": "apiserver.config.k8s.io/v1",
		"kind":       "AuthenticationConfiguration",
		"anonymous":  map[string]any{"enabled": true, "conditions": conditions},
	}, "", "  ")
}

// encryptionConfig encrypts secrets with secretbox; identity still reads secrets written
// before encryption was configured.
func encryptionConfig(key []byte) ([]byte, error) {
	if len(key) != pki.EncryptionKeySize {
		return nil, fmt.Errorf("the encryption key must be %d bytes", pki.EncryptionKeySize)
	}
	return json.MarshalIndent(map[string]any{
		"apiVersion": "apiserver.config.k8s.io/v1",
		"kind":       "EncryptionConfiguration",
		"resources": []any{map[string]any{
			"resources": []string{"secrets"},
			"providers": []any{
				map[string]any{"secretbox": map[string]any{"keys": []any{map[string]any{"name": "chalkos", "secret": base64.StdEncoding.EncodeToString(key)}}}},
				map[string]any{"identity": map[string]any{}},
			},
		}},
	}, "", "  ")
}

// IssueChalkd issues chalkd's client certificate on a control-plane node.
func IssueChalkd(s Share, now time.Time) (pki.CertKey, error) {
	if s.Kind != kubernetes.KindControlPlane {
		return pki.CertKey{}, errors.New("only control-plane nodes hold the CA key")
	}
	return pki.IssueLeaf(s.CA, pki.Leaf{CommonName: ChalkdUser, Organization: []string{MastersGroup}, Client: true}, now)
}
