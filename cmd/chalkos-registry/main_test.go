package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/registry"
)

// TestRun tells systemd the registry is ready once its port takes TLS handshakes, serves the
// catalog the diagnostic prints, and ends without an error when its context ends.
func TestRun(t *testing.T) {
	now := time.Now()
	osCA, err := pki.NewOSCA(now)
	if err != nil {
		t.Fatal(err)
	}
	nodeCA, err := pki.NewNodeCA(osCA, now)
	if err != nil {
		t.Fatal(err)
	}
	node, err := pki.IssueNode(nodeCA, pki.NodeNames{CommonName: "n1", IPs: []net.IP{net.IPv4(127, 0, 0, 1)}}, now)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	c := registry.Config{Storage: filepath.Join(dir, "storage"), Certificate: filepath.Join(dir, "node.pem"), CA: filepath.Join(dir, "ca.crt")}
	if err := atomicfile.WriteFile(c.Certificate, []byte(node.Certificate+node.Key), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.WriteFile(c.CA, []byte(osCA.Certificate), 0o644); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "notify")
	notify, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer notify.Close()
	t.Setenv("NOTIFY_SOCKET", socket)
	// A port that was free a moment ago.
	free, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	port := free.Addr().(*net.TCPAddr).Port
	free.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, port, c) }()
	buf := make([]byte, 64)
	n, err := notify.Read(buf)
	if err != nil || string(buf[:n]) != "READY=1" {
		t.Fatalf("the notification: %q, %v", buf[:n], err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(osCA.Certificate))
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	conn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: roots})
	if err != nil {
		t.Fatalf("a handshake once ready: %v", err)
	}
	conn.Close()
	var out bytes.Buffer
	if err := command(ctx, []string{"catalog", strconv.Itoa(port)}, &out); err != nil || strings.TrimSpace(out.String()) != `{"repositories":[]}` {
		t.Errorf("catalog = %q, %v", out.String(), err)
	}
	for _, args := range [][]string{nil, {"serve", "extra"}, {"serve", "--nope"}, {"catalog"}} {
		if err := command(ctx, args, &out); !errors.Is(err, errUsage) {
			t.Errorf("%q: %v, want the usage", args, err)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("run after its context ended: %v", err)
	}
}
