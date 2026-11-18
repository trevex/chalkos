package chalkctl

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/trevex/chalkos/pkg/registry"
	"github.com/trevex/chalkos/pkg/registry/registrytest"
)

func init() {
	// No line for every request a test makes that a registry refuses.
	registry.QuietLogs()
}

// clusterRegistryDoc serves docker.io from the endpoints 192.0.2.1 and 192.0.2.2.
const clusterRegistryDoc = "apiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: ClusterRegistry\nmetadata: {name: cluster}\nspec: {mirrors: [docker.io], endpoints: [192.0.2.1, 192.0.2.2]}\n"

// registryLab is the test cluster with a ClusterRegistry whose two endpoints registries of the
// cluster's OS CA serve on loopback, and the flags that reach them.
type registryLab struct {
	*testApp
	reach []string
}

func newRegistryLab(t *testing.T) *registryLab {
	t.Helper()
	ta := newTestApp(t)
	writeInventory(t, ta.dir, n1SystemDisk, n1Network, clusterRegistryDoc)
	l := &registryLab{testApp: ta, reach: []string{"--inventory", ta.inventory}}
	for _, e := range []string{"192.0.2.1", "192.0.2.2"} {
		reg := registrytest.Start(t, ta.secrets.OSCA, false, nil, net.ParseIP(e))
		l.reach = append(l.reach, "--connect-to", e+"="+reg.Host)
	}
	return l
}

// readerFile writes a reader's client file and returns its path.
func (l *registryLab) readerFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(l.dir, "reader.json")
	if err := l.run(context.Background(), []string{"client", "new", "--name", "carol", "--role", "reader", "--out", path}); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRegistryPush pushes an OCI layout to both endpoints with the secrets file, and refuses a
// reader's client file.
func TestRegistryPush(t *testing.T) {
	l := newRegistryLab(t)
	img, err := random.Image(1024, 1)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(l.dir, "busybox")
	p, err := layout.Write(dir, empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.AppendImage(img); err != nil {
		t.Fatal(err)
	}
	digest, _ := img.Digest()
	push := append([]string{"registry", "push", dir, "docker.io/library/busybox:1.37.0"}, l.reach...)
	if err := l.run(context.Background(), push); err != nil {
		t.Fatalf("registry push: %v", err)
	}
	for _, want := range []string{
		"192.0.2.1: pushed 192.0.2.1:5000/docker.io/library/busybox:1.37.0 as " + digest.String(),
		"192.0.2.2: pushed 192.0.2.2:5000/docker.io/library/busybox:1.37.0 as " + digest.String(),
	} {
		if !strings.Contains(l.stdout.String(), want) {
			t.Errorf("stdout = %q, want %q", l.stdout, want)
		}
	}
	err = l.run(context.Background(), append(push, "--config", l.readerFile(t)))
	if want := "registry push needs the admin role; the reader role may not"; err == nil || err.Error() != want {
		t.Errorf("registry push with a reader's client file = %v, want %q", err, want)
	}
	err = l.run(context.Background(), append(push, "--connect-to", "192.0.2.9=127.0.0.1:1"))
	if want := "--connect-to names 192.0.2.9, which is no endpoint of the ClusterRegistry; its endpoints are 192.0.2.1 and 192.0.2.2"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("registry push to another endpoint = %v, want %q", err, want)
	}
	err = l.run(context.Background(), append(push, "--connect-to", "192.0.2.1=127.0.0.1:1"))
	if want := "--connect-to names 192.0.2.1 twice"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("registry push with an endpoint named twice = %v, want %q", err, want)
	}
}

// TestRegistryManage lists, deletes and collects garbage on both endpoints; a reader's client
// file lists but neither deletes nor collects.
func TestRegistryManage(t *testing.T) {
	l := newRegistryLab(t)
	img, err := random.Image(1024, 1)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(l.dir, "busybox.tar")
	tag, _ := name.NewTag("busybox:1.37.0")
	if err := tarball.WriteToFile(archive, tag, img); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		l.stdout.Reset()
		return l.run(context.Background(), append(args, l.reach...))
	}
	if err := run("registry", "push", archive, "docker.io/library/busybox:1.37.0"); err != nil {
		t.Fatalf("registry push: %v", err)
	}
	if err := run("registry", "list"); err != nil || !strings.Contains(l.stdout.String(), "docker.io/library/busybox  1.37.0  sha256:") || !strings.HasSuffix(l.stdout.String(), "  all\n") {
		t.Errorf("registry list = %v, %q, want busybox on all endpoints", err, l.stdout)
	}
	reader := l.readerFile(t)
	if err := run("registry", "list", "--config", reader); err != nil {
		t.Errorf("registry list with a reader's client file: %v", err)
	}
	for _, cmd := range [][]string{{"registry", "delete", "busybox:1.37.0"}, {"registry", "gc"}} {
		err := run(append(cmd, "--config", reader)...)
		if want := strings.Join(cmd[:2], " ") + " needs the admin role; the reader role may not"; err == nil || err.Error() != want {
			t.Errorf("%s with a reader's client file = %v, want %q", strings.Join(cmd, " "), err, want)
		}
	}
	digest, _ := img.Digest()
	if err := run("registry", "delete", "busybox@"+digest.String()); err == nil || !strings.Contains(err.Error(), "pass --with-tags") {
		t.Errorf("registry delete of a tagged digest = %v, want it refused", err)
	}
	if err := run("registry", "delete", "busybox@"+digest.String(), "--with-tags"); err != nil || strings.Count(l.stdout.String(), " and its tags 1.37.0\n") != 2 {
		t.Errorf("registry delete --with-tags = %v, %q, want the digest and its tag deleted on both endpoints", err, l.stdout)
	}
	if err := run("registry", "list"); err != nil || l.stdout.String() != "REPOSITORY  TAG  DIGEST  ENDPOINTS\n" {
		t.Errorf("registry list after the delete = %v, %q, want the header alone", err, l.stdout)
	}
	if err := run("registry", "gc"); err != nil || strings.Count(l.stdout.String(), ": freed ") != 2 || strings.Contains(l.stdout.String(), "freed 0 B") {
		t.Errorf("registry gc = %v, %q, want the space freed on both endpoints", err, l.stdout)
	}
}
