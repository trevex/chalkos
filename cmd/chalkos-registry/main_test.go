package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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
	if err := os.WriteFile(filepath.Join(dir, "node.pem"), []byte(node.Certificate+node.Key), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), []byte(osCA.Certificate), 0o644); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "notify")
	notify, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer notify.Close()
	t.Setenv("NOTIFY_SOCKET", socket)
	s, err := registry.New(t.Context(), registry.Config{Storage: filepath.Join(dir, "storage"), Certificate: filepath.Join(dir, "node.pem"), CA: filepath.Join(dir, "ca.crt")})
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, l, s) }()
	buf := make([]byte, 64)
	n, err := notify.Read(buf)
	if err != nil || string(buf[:n]) != "READY=1" {
		t.Fatalf("the notification: %q, %v", buf[:n], err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(osCA.Certificate))
	conn, err := tls.Dial("tcp", l.Addr().String(), &tls.Config{RootCAs: roots, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatalf("a handshake once ready: %v", err)
	}
	if p := conn.ConnectionState().NegotiatedProtocol; p != "h2" {
		t.Errorf("negotiated %q, want h2", p)
	}
	conn.Close()
	var out bytes.Buffer
	if err := command(ctx, []string{"catalog", port}, &out); err != nil || strings.TrimSpace(out.String()) != `{"repositories":[]}` {
		t.Errorf("catalog = %q, %v", out.String(), err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("run after its context ended: %v", err)
	}
}

// TestCommand refuses command lines it does not take, and serve without the files of its
// credentials.
func TestCommand(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{nil, {"serve", "extra"}, {"serve", "--nope"}, {"catalog"}} {
		if err := command(t.Context(), args, &out); !errors.Is(err, errUsage) {
			t.Errorf("%q: %v, want the usage", args, err)
		}
	}
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	if err := command(t.Context(), []string{"serve"}, &out); err == nil || !strings.Contains(err.Error(), "no credentials directory") {
		t.Errorf("serve without credentials: %v", err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", t.TempDir())
	if err := command(t.Context(), []string{"serve", "--storage", t.TempDir()}, &out); err == nil || !strings.Contains(err.Error(), "the node certificate") {
		t.Errorf("serve with empty credentials: %v", err)
	}
}
