// Package etcdtest runs etcd members inside a test, with mutual TLS from a test CA as on a
// control-plane node.
package etcdtest

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.etcd.io/etcd/client/pkg/v3/transport"
	"go.etcd.io/etcd/server/v3/embed"
	"go.uber.org/zap"

	"github.com/trevex/chalkos/pkg/pki"
)

// NewCA returns a CA for the members and their clients.
func NewCA(t testing.TB) pki.CertKey {
	t.Helper()
	k, err := pki.NewKubernetesSecrets(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return k.EtcdCA
}

// ClientTLS is the TLS configuration of a client with a certificate from ca.
func ClientTLS(t testing.TB, ca pki.CertKey) *tls.Config {
	t.Helper()
	ck, err := pki.IssueLeaf(ca, pki.Leaf{CommonName: "client", Client: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair([]byte(ck.Certificate), []byte(ck.Key))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(ca.Certificate))
	return &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: roots, MinVersion: tls.VersionTLS12}
}

// PeerURL returns a peer URL on a free port, to add a member with before it starts.
func PeerURL(t testing.TB) string {
	t.Helper()
	return "https://127.0.0.1:" + freePort(t)
}

// Member is a running etcd member.
type Member struct {
	Name      string
	ClientURL string
	PeerURL   string
	etcd      *embed.Etcd
	stop      sync.Once
}

// StartNew starts the only member of a new cluster.
func StartNew(t testing.TB, ca pki.CertKey, name string) *Member {
	t.Helper()
	peerURL := PeerURL(t)
	return start(t, ca, name, peerURL, name+"="+peerURL, embed.ClusterStateFlagNew)
}

// StartExisting starts a member that was added to a cluster at peerURL, as etcd's
// --initial-cluster-state=existing does with the initial cluster.
func StartExisting(t testing.TB, ca pki.CertKey, name, peerURL, initialCluster string) *Member {
	t.Helper()
	return start(t, ca, name, peerURL, initialCluster, embed.ClusterStateFlagExisting)
}

func start(t testing.TB, ca pki.CertKey, name, peerURL, initialCluster, state string) *Member {
	t.Helper()
	dir := t.TempDir()
	peer := parse(t, peerURL)
	tlsInfo := serverTLS(t, ca, name, net.ParseIP(peer.Hostname()), dir)
	clientURL := "https://127.0.0.1:" + freePort(t)
	cfg := embed.NewConfig()
	cfg.Name = name
	cfg.Dir = filepath.Join(dir, "data")
	cfg.ListenClientUrls = []url.URL{parse(t, clientURL)}
	cfg.AdvertiseClientUrls = cfg.ListenClientUrls
	cfg.ListenPeerUrls = []url.URL{peer}
	cfg.AdvertisePeerUrls = cfg.ListenPeerUrls
	cfg.InitialCluster = initialCluster
	cfg.ClusterState = state
	cfg.ClientTLSInfo = tlsInfo
	cfg.PeerTLSInfo = tlsInfo
	cfg.ZapLoggerBuilder = embed.NewZapLoggerBuilder(zap.NewNop())
	// Short ticks keep elections and the leader hand-over on stop quick.
	cfg.TickMs, cfg.ElectionMs = 10, 100
	e, err := embed.StartEtcd(cfg)
	if err != nil {
		t.Fatalf("start etcd member %s: %v", name, err)
	}
	m := &Member{Name: name, ClientURL: clientURL, PeerURL: peerURL, etcd: e}
	t.Cleanup(m.Stop)
	select {
	case <-e.Server.ReadyNotify():
	case err := <-e.Err():
		t.Fatalf("etcd member %s: %v", name, err)
	case <-time.After(time.Minute):
		t.Fatalf("etcd member %s did not become ready", name)
	}
	return m
}

// Stop stops the member; its data stays.
func (m *Member) Stop() { m.stop.Do(m.etcd.Close) }

// serverTLS writes a certificate for the member's client connections on 127.0.0.1 and its peer
// connections on peerIP to dir.
func serverTLS(t testing.TB, ca pki.CertKey, name string, peerIP net.IP, dir string) transport.TLSInfo {
	t.Helper()
	ips := []net.IP{net.IPv4(127, 0, 0, 1), peerIP}
	ck, err := pki.IssueLeaf(ca, pki.Leaf{CommonName: name, IPs: ips, Server: true, Client: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"ca.crt": ca.Certificate, "member.crt": ck.Certificate, "member.key": ck.Key}
	for file, data := range files {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return transport.TLSInfo{
		CertFile:       filepath.Join(dir, "member.crt"),
		KeyFile:        filepath.Join(dir, "member.key"),
		TrustedCAFile:  filepath.Join(dir, "ca.crt"),
		ClientCertAuth: true,
	}
}

func freePort(t testing.TB) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return port
}

func parse(t testing.TB, s string) url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return *u
}
