package etcdtest

import (
	"errors"
	"net"
	"syscall"
	"testing"

	"go.etcd.io/etcd/server/v3/embed"
)

// A port another listener took makes etcd fail with EADDRINUSE, which start tries again on new
// ports for.
func TestStartEtcdOnATakenPort(t *testing.T) {
	ca := NewCA(t)
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	peerURL := PeerURL(t)
	e, err := startEtcd(t, ca, "m1", peerURL, peerURL, "https://"+taken.Addr().String(), "m1="+peerURL, embed.ClusterStateFlagNew)
	if err == nil {
		e.Close()
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Errorf("start on a taken port: %v, want EADDRINUSE", err)
	}
}

// A reserved port stays taken until it is released.
func TestReserve(t *testing.T) {
	addr := reserve(t)
	if l, err := net.Listen("tcp", addr); err == nil {
		l.Close()
		t.Fatal("a reserved port was free")
	}
	release("https://" + addr)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("a released port: %v", err)
	}
	l.Close()
}
