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
	"go.etcd.io/etcd/server/v3/storage/wal"
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
	return start(t, ca, name, peerURL, peerURL, name+"="+peerURL, embed.ClusterStateFlagNew)
}

// StartAdvertising starts the only member of a new cluster that tells its peers to reach it at
// peerURL, such as a node's address, while it listens on a free local port.
func StartAdvertising(t testing.TB, ca pki.CertKey, name, peerURL string) *Member {
	t.Helper()
	return start(t, ca, name, PeerURL(t), peerURL, name+"="+peerURL, embed.ClusterStateFlagNew)
}

// StartExisting starts a member that was added to a cluster at peerURL, as etcd's
// --initial-cluster-state=existing does with the initial cluster.
func StartExisting(t testing.TB, ca pki.CertKey, name, peerURL, initialCluster string) *Member {
	t.Helper()
	return start(t, ca, name, peerURL, peerURL, initialCluster, embed.ClusterStateFlagExisting)
}

func start(t testing.TB, ca pki.CertKey, name, listenPeerURL, peerURL, initialCluster, state string) *Member {
	t.Helper()
	dir := memDir(t)
	peer := parse(t, peerURL)
	tlsInfo := serverTLS(t, ca, name, net.ParseIP(peer.Hostname()), dir)
	clientURL := "https://127.0.0.1:" + freePort(t)
	cfg := embed.NewConfig()
	cfg.Name = name
	cfg.Dir = filepath.Join(dir, "data")
	cfg.ListenClientUrls = []url.URL{parse(t, clientURL)}
	cfg.AdvertiseClientUrls = cfg.ListenClientUrls
	cfg.ListenPeerUrls = []url.URL{parse(t, listenPeerURL)}
	cfg.AdvertisePeerUrls = []url.URL{peer}
	cfg.InitialCluster = initialCluster
	cfg.ClusterState = state
	cfg.ClientTLSInfo = tlsInfo
	cfg.PeerTLSInfo = tlsInfo
	cfg.ZapLoggerBuilder = embed.NewZapLoggerBuilder(zap.NewNop())
	// Short ticks keep elections and the leader hand-over on stop quick.
	cfg.TickMs, cfg.ElectionMs = 10, 100
	// The data is thrown away after the test; syncing it would stall the members, and with them
	// elections and member changes, whenever the disk is busy.
	cfg.UnsafeNoFsync = true
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

func init() {
	// A member preallocates two WAL segments, of 64 MB each by default, in memory with memDir; the
	// tests write a fraction of a segment.
	wal.SegmentSizeBytes = 1 << 20
}

// memDir returns a directory for a member's data in memory where the system has one, and a
// temporary directory otherwise. Unsynced writes still go through the page cache, and stall the
// member for seconds while a busy disk holds back writeback.
func memDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("/dev/shm", "etcdtest")
	if err != nil {
		return t.TempDir()
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
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

// Silent returns a client URL whose listener accepts connections and never answers, as a
// partitioned member's does.
func Silent(t testing.TB) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range conns {
			conn.Close()
		}
	})
	return "https://" + l.Addr().String()
}

// Proxy forwards connections to a member's client URL, delaying what the member sends by Delay,
// as a slow network does. Once paused it forwards nothing more but keeps the connections open, as
// a network partition does.
type Proxy struct {
	// URL is the client URL to dial instead of the member's.
	URL    string
	delay  time.Duration
	paused chan struct{}
	pause  sync.Once
	closed chan struct{}
}

// NewProxy forwards connections to target, delaying the replies by delay.
func NewProxy(t testing.TB, target string, delay time.Duration) *Proxy {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := parse(t, target)
	p := &Proxy{URL: "https://" + l.Addr().String(), delay: delay, paused: make(chan struct{}), closed: make(chan struct{})}
	var mu sync.Mutex
	var conns []net.Conn
	track := func(c net.Conn) {
		mu.Lock()
		defer mu.Unlock()
		conns = append(conns, c)
	}
	go func() {
		for {
			client, err := l.Accept()
			if err != nil {
				return
			}
			track(client)
			member, err := net.Dial("tcp", u.Host)
			if err != nil {
				client.Close()
				continue
			}
			track(member)
			go p.forward(member, client, 0)
			go p.forward(client, member, p.delay)
		}
	}()
	t.Cleanup(func() {
		close(p.closed)
		l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	return p
}

// Pause stops forwarding.
func (p *Proxy) Pause() { p.pause.Do(func() { close(p.paused) }) }

// forward copies from src to dst, each chunk delay after it arrived, until the proxy is paused.
func (p *Proxy) forward(dst, src net.Conn, delay time.Duration) {
	type chunk struct {
		data []byte
		at   time.Time
	}
	chunks := make(chan chunk, 1024)
	go func() {
		defer close(chunks)
		for {
			buf := make([]byte, 32*1024)
			n, err := src.Read(buf)
			if n > 0 {
				select {
				case chunks <- chunk{buf[:n], time.Now().Add(delay)}:
				case <-p.closed:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	for c := range chunks {
		select {
		case <-time.After(time.Until(c.at)):
		case <-p.closed:
			return
		}
		select {
		case <-p.paused:
			// Hold the connection open without delivering anything.
			<-p.closed
			return
		default:
		}
		if _, err := dst.Write(c.data); err != nil {
			return
		}
	}
}
