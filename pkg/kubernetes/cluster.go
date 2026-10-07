// Package kubernetes describes a chalkos node's Kubernetes cluster as its image records it in
// /etc/chalkos/kubernetes/cluster.json, and the node itself as its identity names it.
package kubernetes

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"

	"github.com/trevex/chalkos/pkg/manifest"
)

// Kinds of Kubernetes nodes.
const (
	KindControlPlane = manifest.KindControlPlane
	KindWorker       = manifest.KindWorker
)

// Cluster is what the node's image knows about its cluster.
type Cluster struct {
	// Kind is what the image's nodes are: KindControlPlane or KindWorker.
	Kind string `json:"kind"`
	// Endpoint is the API server's URL, such as https://10.0.0.10:6443.
	Endpoint string `json:"endpoint"`
	// Version is the Kubernetes release, such as 1.37.1.
	Version                        string `json:"version"`
	PodCIDR                        string `json:"podCIDR"`
	ServiceCIDR                    string `json:"serviceCIDR"`
	DNSIP                          string `json:"dnsIP"`
	Domain                         string `json:"domain"`
	AllowSchedulingOnControlPlanes bool   `json:"allowSchedulingOnControlPlanes"`
	// ExtraArgs are flags of the control-plane components by component name, such as
	// kube-apiserver, without leading dashes.
	ExtraArgs map[string]map[string]string `json:"extraArgs"`
	Images    Images                       `json:"images"`
}

// Images are the control plane's images.
type Images struct {
	Etcd                  string `json:"etcd"`
	KubeAPIServer         string `json:"kubeAPIServer"`
	KubeControllerManager string `json:"kubeControllerManager"`
	KubeScheduler         string `json:"kubeScheduler"`
}

// ReadCluster reads and checks the image's cluster file.
func ReadCluster(path string) (Cluster, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Cluster{}, err
	}
	var c Cluster
	if err := json.Unmarshal(data, &c); err != nil {
		return Cluster{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Cluster{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// ProtectedAPIServerFlags decide who may do what on the API server; chalkos sets them and
// extra flags must not override them.
var ProtectedAPIServerFlags = []string{"anonymous-auth", "authentication-config", "authorization-mode", "enable-bootstrap-token-auth"}

// Validate checks the kind, the endpoint, the address ranges and the extra flags.
func (c Cluster) Validate() error {
	for _, flag := range ProtectedAPIServerFlags {
		if _, ok := c.ExtraArgs["kube-apiserver"][flag]; ok {
			return fmt.Errorf("extraArgs: kube-apiserver --%s is set by chalkos and cannot be overridden", flag)
		}
	}
	if c.Kind != KindControlPlane && c.Kind != KindWorker {
		return fmt.Errorf("unknown kind %q", c.Kind)
	}
	if _, err := c.EndpointHost(); err != nil {
		return err
	}
	if _, err := c.APIServerServiceIP(); err != nil {
		return err
	}
	if _, _, err := net.ParseCIDR(c.PodCIDR); err != nil {
		return fmt.Errorf("podCIDR: %w", err)
	}
	if net.ParseIP(c.DNSIP) == nil {
		return fmt.Errorf("dnsIP %q is not an address", c.DNSIP)
	}
	if c.Domain == "" {
		return errors.New("no cluster domain")
	}
	return nil
}

// EndpointHost returns the host of the endpoint, a name or an address.
func (c Cluster) EndpointHost() (string, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", fmt.Errorf("endpoint %q is not an https URL", c.Endpoint)
	}
	return u.Hostname(), nil
}

// APIServerServiceIP returns the first address of the service range, which the kubernetes
// service gets.
func (c Cluster) APIServerServiceIP() (net.IP, error) {
	_, cidr, err := net.ParseCIDR(c.ServiceCIDR)
	if err != nil {
		return nil, fmt.Errorf("serviceCIDR: %w", err)
	}
	ip := cidr.IP
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	n := new(big.Int).Add(new(big.Int).SetBytes(ip), big.NewInt(1))
	out := make(net.IP, len(ip))
	n.FillBytes(out)
	return out, nil
}

// Node is this node as its identity describes it.
type Node struct {
	// Name is the node's name in the cluster and in Kubernetes.
	Name string
	// Hostname is the node's host name, which may differ from its name.
	Hostname string
	// IP is the address the kubelet registers; nil lets the kubelet choose.
	IP net.IP
	// Addresses are the node's static addresses.
	Addresses []net.IP
	Labels    map[string]string
	Taints    []manifest.Taint
}

// ReadNode reads the node from its identity without secrets (node.json).
func ReadNode(path string) (Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Node{}, err
	}
	n, err := ParseNode(data)
	if err != nil {
		return Node{}, fmt.Errorf("%s: %w", path, err)
	}
	return n, nil
}

// ParseNode reads the node from its identity.
func ParseNode(identity []byte) (Node, error) {
	var id manifest.Identity
	if err := json.Unmarshal(identity, &id); err != nil {
		return Node{}, fmt.Errorf("parse the identity: %w", err)
	}
	if id.Kubernetes == nil || id.Kubernetes.NodeName == "" {
		return Node{}, errors.New("the identity names no Kubernetes node")
	}
	n := Node{Name: id.Kubernetes.NodeName, Hostname: id.Hostname, Labels: id.Labels, Taints: id.Taints}
	if id.Kubernetes.NodeIP != "" {
		if n.IP = net.ParseIP(id.Kubernetes.NodeIP); n.IP == nil {
			return Node{}, fmt.Errorf("nodeIP %q is not an address", id.Kubernetes.NodeIP)
		}
	}
	for _, a := range id.StaticAddresses() {
		if ip := net.ParseIP(a); ip != nil {
			n.Addresses = append(n.Addresses, ip)
		}
	}
	return n, nil
}

// IPs returns the node's address and its static addresses, each once.
func (n Node) IPs() []net.IP {
	var ips []net.IP
	for _, ip := range append([]net.IP{n.IP}, n.Addresses...) {
		if ip == nil {
			continue
		}
		seen := false
		for _, have := range ips {
			seen = seen || have.Equal(ip)
		}
		if !seen {
			ips = append(ips, ip)
		}
	}
	return ips
}
