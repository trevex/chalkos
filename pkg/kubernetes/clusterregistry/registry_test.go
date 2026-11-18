package clusterregistry

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

// TestNew lower-cases the mirrored hosts, names Docker Hub docker.io, reads the endpoints as
// addresses and takes port 5000 by default.
func TestNew(t *testing.T) {
	r, err := New(Spec{Mirrors: []string{"Docker.io", "index.docker.io.example", "ghcr.io"}, Endpoints: []string{"fd00::11", "192.0.2.12"}})
	if err != nil {
		t.Fatal(err)
	}
	want := Registry{
		Mirrors:   []string{"docker.io", "index.docker.io.example", "ghcr.io"},
		Endpoints: []netip.Addr{netip.MustParseAddr("fd00::11"), netip.MustParseAddr("192.0.2.12")},
		Port:      DefaultPort,
	}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("New = %+v, want %+v", r, want)
	}
	if r, err := New(Spec{Mirrors: []string{"index.docker.io"}, Endpoints: []string{"::ffff:192.0.2.1"}, Port: 5001}); err != nil || r.Mirrors[0] != "docker.io" || r.Endpoints[0] != netip.MustParseAddr("192.0.2.1") || r.Port != 5001 {
		t.Errorf("New = %+v, %v", r, err)
	}
}

// TestNewRefuses refuses what the kind's schema lets through but a repository path or an
// address does not take, and what repeats under another spelling.
func TestNewRefuses(t *testing.T) {
	for _, tc := range []struct {
		spec Spec
		want string
	}{
		{Spec{Endpoints: []string{"fd00::11"}}, "mirrors: none"},
		{Spec{Mirrors: []string{"docker.io"}}, "endpoints: none"},
		{Spec{Mirrors: []string{"a.-b"}, Endpoints: []string{"fd00::11"}}, `"a.-b" cannot start a repository path`},
		{Spec{Mirrors: []string{"docker.io", "index.docker.io"}, Endpoints: []string{"fd00::11"}}, "docker.io and index.docker.io name the same registry docker.io"},
		{Spec{Mirrors: []string{"docker.io"}, Endpoints: []string{"fe80::1%eth0"}}, `"fe80::1%eth0" is no IP address without a zone`},
		{Spec{Mirrors: []string{"docker.io"}, Endpoints: []string{"fd00::11", "fd00:0::11"}}, "fd00::11 and fd00:0::11 are the same address"},
		{Spec{Mirrors: []string{"docker.io"}, Endpoints: []string{"fd00::11"}, Port: 70000}, "port: 70000 is no TCP port"},
		{Spec{Mirrors: []string{"docker.io"}, Endpoints: []string{"fd00::11"}, Port: 443}, "port: 443 is no TCP port from 1024"},
		{Spec{Mirrors: []string{"a..b"}, Endpoints: []string{"fd00::11"}}, `"a..b" cannot start a repository path`},
		{Spec{Mirrors: []string{"foo-.bar"}, Endpoints: []string{"fd00::11"}}, `"foo-.bar" cannot start a repository path`},
	} {
		if _, err := New(tc.spec); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("New(%+v) = %v, want %q", tc.spec, err, tc.want)
		}
	}
}

// TestMirrorURLs orders the endpoints with the node's own first, and names each host's copy below
// the registry API's root, an IPv6 endpoint in brackets.
func TestMirrorURLs(t *testing.T) {
	r, err := New(Spec{Mirrors: []string{"docker.io", "ghcr.io"}, Endpoints: []string{"fd00::11", "192.0.2.12", "fd00::13"}})
	if err != nil {
		t.Fatal(err)
	}
	own := Addrs([]string{"10.0.0.1", "192.0.2.12", "not an address"})
	if e, ok := r.Serving(own); !ok || e != netip.MustParseAddr("192.0.2.12") {
		t.Errorf("Serving = %v, %v", e, ok)
	}
	if _, ok := r.Serving(Addrs([]string{"10.0.0.1"})); ok {
		t.Error("a node of none of the endpoints serves")
	}
	want := map[string][]string{
		"docker.io": {"https://192.0.2.12:5000/v2/docker.io", "https://[fd00::11]:5000/v2/docker.io", "https://[fd00::13]:5000/v2/docker.io"},
		"ghcr.io":   {"https://192.0.2.12:5000/v2/ghcr.io", "https://[fd00::11]:5000/v2/ghcr.io", "https://[fd00::13]:5000/v2/ghcr.io"},
	}
	if got := r.MirrorURLs(own); !reflect.DeepEqual(got, want) {
		t.Errorf("MirrorURLs = %v, want %v", got, want)
	}
	if got := r.MirrorURLs(nil)["ghcr.io"][0]; got != "https://[fd00::11]:5000/v2/ghcr.io" {
		t.Errorf("a node that serves none starts at %s", got)
	}
}
