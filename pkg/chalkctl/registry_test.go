package chalkctl

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/random"

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
}
