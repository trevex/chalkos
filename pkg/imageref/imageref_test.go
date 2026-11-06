package imageref

import (
	"strings"
	"testing"
)

const digest = "sha256:e190f914a6cc21cab9268485d1ae7282fdeab1315ac64b668aa42d732fd1061d"

func TestParse(t *testing.T) {
	for s, want := range map[string]Reference{
		"registry.k8s.io/kube-apiserver:v1.37.1":               {"v1.37.1", ""},
		"registry.k8s.io/kube-apiserver:v1.37.1@" + digest:     {"v1.37.1", digest},
		"registry.k8s.io/kube-apiserver@" + digest:             {"", digest},
		"localhost:5000/kube-apiserver:v1.37.1@" + digest:      {"v1.37.1", digest},
		"registry.k8s.io/coredns/coredns:v1.14.6":              {"v1.14.6", ""},
		"registry.k8s.io/etcd:3.7.0-0@" + digest:               {"3.7.0-0", digest},
		"[2001:db8::1]:5000/kube-proxy:v1.37.1-rc.1@" + digest: {"v1.37.1-rc.1", digest},
	} {
		got, err := Parse(s)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", s, got, err, want)
		}
	}
	for _, s := range []string{
		"", "kube-apiserver:v1.37.1", "registry.k8s.io/kube-apiserver", "registry.k8s.io/Kube:v1",
		"registry.k8s.io/kube-apiserver:v1.37.1@sha256:abc", "registry.k8s.io/kube-apiserver:@" + digest,
		"https://registry.k8s.io/kube-apiserver:v1.37.1", "registry.k8s.io/x:v1@" + digest + "@" + digest,
	} {
		if r, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) = %+v, want an error", s, r)
		}
	}
}

func TestVersion(t *testing.T) {
	for tag, want := range map[string]Version{
		"v1.37.1":      {1, 37, 1, ""},
		"v1.38.0-rc.1": {1, 38, 0, "-rc.1"},
		"v0.0.0":       {0, 0, 0, ""},
	} {
		got, err := Reference{Tag: tag}.Version()
		if err != nil || got != want {
			t.Errorf("version of %q = %+v, %v; want %+v", tag, got, err, want)
		}
		if got.String() != tag {
			t.Errorf("String() = %q, want %q", got.String(), tag)
		}
	}
	for _, tag := range []string{"", "1.37.1", "v1.37", "v1.37.1.2", "v01.37.1", "latest", "v1.37.1-", "v1.37.1+build"} {
		if _, err := (Reference{Tag: tag}).Version(); err == nil || !strings.Contains(err.Error(), "no version") {
			t.Errorf("version of %q: err = %v, want ErrNoVersion", tag, err)
		}
	}
}
