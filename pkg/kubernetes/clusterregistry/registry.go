// Package clusterregistry is the Kubernetes layer's cluster registry: the ClusterRegistry kind as
// Go reads it and the endpoints containerd pulls its hosts' images from. It takes the standard
// library alone, so that chalkos-render can link it.
package clusterregistry

import (
	"cmp"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
)

const (
	// Kind is the ClusterRegistry's kind in kubernetes.chalkos.dev/v1alpha1.
	Kind = "ClusterRegistry"
	// DefaultPort is the port the kind's schema defaults to.
	DefaultPort = 5000
)

// Spec is a ClusterRegistry's spec as written.
type Spec struct {
	Mirrors   []string `json:"mirrors"`
	Endpoints []string `json:"endpoints"`
	Port      int      `json:"port"`
}

// Registry is a ClusterRegistry: the prefixes of the hosts it mirrors, its endpoints in pull
// order, and its port.
type Registry struct {
	// Mirrors are the mirrored hosts as Prefix names them.
	Mirrors   []string
	Endpoints []netip.Addr
	Port      int
}

// repoComponent is a component of a repository path, as the OCI distribution spec takes it.
var repoComponent = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)

// Prefix is the path below which the cluster registry holds the images of host, and the directory
// containerd looks host's hosts.toml up in: host in lower case, and docker.io for Docker Hub. A
// host a repository path cannot start with, such as one with a port, is refused.
func Prefix(host string) (string, error) {
	p := strings.ToLower(host)
	if p == "index.docker.io" {
		p = "docker.io"
	}
	if !repoComponent.MatchString(p) {
		return "", fmt.Errorf("%q cannot start a repository path; name a registry host without a port", host)
	}
	return p, nil
}

// New reads a ClusterRegistry's spec.
func New(s Spec) (Registry, error) {
	if len(s.Mirrors) == 0 {
		return Registry{}, errors.New("mirrors: none; name the registry hosts the cluster registry serves")
	}
	if len(s.Endpoints) == 0 {
		return Registry{}, errors.New("endpoints: none; name the addresses of the nodes that serve the cluster registry")
	}
	r := Registry{Port: cmp.Or(s.Port, DefaultPort)}
	if r.Port < 1 || r.Port > 65535 {
		return Registry{}, fmt.Errorf("port: %d is no TCP port", r.Port)
	}
	named := map[string]string{}
	for _, m := range s.Mirrors {
		p, err := Prefix(m)
		if err != nil {
			return Registry{}, fmt.Errorf("mirrors: %w", err)
		}
		if other, ok := named[p]; ok {
			return Registry{}, fmt.Errorf("mirrors: %s and %s name the same registry %s; name each host once", other, m, p)
		}
		named[p] = m
		r.Mirrors = append(r.Mirrors, p)
	}
	written := map[netip.Addr]string{}
	for _, e := range s.Endpoints {
		a, err := netip.ParseAddr(e)
		if err != nil || a.Zone() != "" {
			return Registry{}, fmt.Errorf("endpoints: %q is no IP address without a zone", e)
		}
		a = a.Unmap()
		if other, ok := written[a]; ok {
			return Registry{}, fmt.Errorf("endpoints: %s and %s are the same address; name each address once", other, e)
		}
		written[a] = e
		r.Endpoints = append(r.Endpoints, a)
	}
	return r, nil
}

// Addrs reads a node's static addresses, as the identity lists them: an IPv4-mapped one in its
// IPv4 form, and none it cannot read.
func Addrs(static []string) []netip.Addr {
	var out []netip.Addr
	for _, s := range static {
		if a, err := netip.ParseAddr(s); err == nil {
			out = append(out, a.Unmap())
		}
	}
	return out
}

// Serving returns the first endpoint among the node's static addresses; ok is false on a node
// that serves the cluster registry at none.
func (r Registry) Serving(static []netip.Addr) (endpoint netip.Addr, ok bool) {
	for _, e := range r.Endpoints {
		if slices.Contains(static, e) {
			return e, true
		}
	}
	return netip.Addr{}, false
}

// Ordered are the endpoints in the order a node with the static addresses pulls from them: the
// one it serves first, then the others in the order of endpoints.
func (r Registry) Ordered(static []netip.Addr) []netip.Addr {
	own, ok := r.Serving(static)
	if !ok {
		return r.Endpoints
	}
	out := []netip.Addr{own}
	for _, e := range r.Endpoints {
		if e != own {
			out = append(out, e)
		}
	}
	return out
}

// Host is an endpoint's host and port as URLs and image references name it, an IPv6 address in
// brackets.
func (r Registry) Host(e netip.Addr) string {
	return netip.AddrPortFrom(e, uint16(r.Port)).String()
}

// MirrorURLs returns, by prefix, the URLs of each mirrored host's copy in the cluster registry, in
// the order a node with the static addresses pulls from them: the registry API's root below which
// the host's repositories lie, https://<endpoint>/v2/<prefix>.
func (r Registry) MirrorURLs(static []netip.Addr) map[string][]string {
	out := map[string][]string{}
	ordered := r.Ordered(static)
	for _, p := range r.Mirrors {
		for _, e := range ordered {
			out[p] = append(out[p], "https://"+r.Host(e)+"/v2/"+p)
		}
	}
	return out
}
