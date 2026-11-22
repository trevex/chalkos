package chalkctl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unpinnedProxy is the warning of a Cluster document whose kube-proxy names a tag but no digest.
const unpinnedProxy = `Cluster "default": images.proxy: registry.k8s.io/kube-proxy:v1.37.1 names a tag but no digest, so nodes pull whatever the tag points to when they pull; pin it as registry.k8s.io/kube-proxy:v1.37.1@sha256:<digest>`

// unpinProxy drops the digest of kube-proxy's image from the cluster.yaml in dir.
func unpinProxy(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "cluster.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pinned := string(data)
	unpinned := strings.Replace(pinned, "kube-proxy:v1.37.1@sha256:b9ae667ed9c160c9d5b66f55e8afec97fc5483a12db0ada5286541b0385cb9f4", "kube-proxy:v1.37.1", 1)
	if unpinned == pinned {
		t.Fatalf("%s names no pinned kube-proxy", path)
	}
	writeFile(t, path, unpinned)
}

// validate warns of an image that names a tag but no digest, and the node is valid all the same.
func TestValidateWarnsOfUnpinnedImages(t *testing.T) {
	ta := newTestApp(t)
	ta.withoutSecrets(t)
	unpinProxy(t, ta.dir)
	closeVXLAN(t, ta.dir)
	if err := ta.run(context.Background(), []string{"validate", "--inventory", ta.inventory}); err != nil {
		t.Fatal(err)
	}
	if want := "warning: node n1: " + unpinnedProxy + "\n"; ta.stderr.String() != want {
		t.Errorf("stderr = %q, want %q", ta.stderr, want)
	}
	if !strings.Contains(ta.stdout.String(), "n1: valid") {
		t.Errorf("stdout = %q, want n1 valid", ta.stdout)
	}
}

// config apply warns of an image that names a tag but no digest, and delivers the config all the
// same.
func TestApplyConfigWarnsOfUnpinnedImages(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	unpinProxy(t, ta.dir)
	if err := ta.run(context.Background(), ta.args([]string{"config", "apply", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if want := "chalkctl: warning: n1: " + unpinnedProxy + "\n"; ta.stderr.String() != want {
		t.Errorf("stderr = %q, want %q", ta.stderr, want)
	}
	if !strings.Contains(ta.stdout.String(), "n1 runs config "+ta.configVersion(t)) {
		t.Errorf("stdout = %q, want the config delivered", ta.stdout)
	}
}
