package kubernetes

import (
	"net"
	"net/netip"
	"strconv"
)

// Ports the control plane's components listen on.
const (
	// APIServerPort is the API server's port, on every address of a control-plane node.
	APIServerPort = 6443
	// EtcdClientPort is where etcd's clients reach a member.
	EtcdClientPort = 2379
	// EtcdPeerPort is where etcd's peers reach a member.
	EtcdPeerPort = 2380
	// EtcdMetricsPort serves etcd's metrics and health on the loopback address.
	EtcdMetricsPort = 2381
)

// EtcdClientURL is the URL clients reach the etcd member on the node with the address at.
func EtcdClientURL(ip netip.Addr) string { return httpsURL(ip, EtcdClientPort) }

// EtcdPeerURL is the URL etcd's peers reach the member on the node with the address at.
func EtcdPeerURL(ip netip.Addr) string { return httpsURL(ip, EtcdPeerPort) }

func httpsURL(ip netip.Addr, port int) string {
	return "https://" + net.JoinHostPort(ip.String(), strconv.Itoa(port))
}
