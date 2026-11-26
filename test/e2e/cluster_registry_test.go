package e2e

import (
	"archive/tar"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes"
)

// The cluster registry's port on cp1, which the lab file forwards; the host the cluster registry
// mirrors, which resolves nowhere and has no Mirror, so its images come from the cluster registry
// alone; and the image the test pushes there.
const (
	clusterRegistryPort  = 5000
	clusterRegistryHost  = "cluster-registry.chalkos.test"
	clusterRegistryRepo  = clusterRegistryHost + "/e2e/busybox"
	clusterRegistryImage = clusterRegistryRepo + ":1.37.0"
)

// clusterRegistryDocs make cp1 serve the cluster registry for clusterRegistryHost, whose Registry
// has credentials, which containerd may send to the registry and the registry ignores on reads.
// The Secret registry is the private registry's, in groups/registries.yaml.
const clusterRegistryDocs = `---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: ClusterRegistry
metadata: {name: cluster}
spec:
  mirrors: [` + clusterRegistryHost + `]
  endpoints: [192.168.100.11]
---
apiVersion: oci.chalkos.dev/v1alpha1
kind: Registry
metadata: {name: cluster-registry}
spec:
  host: ` + clusterRegistryHost + `
  username: e2e
  password: {secretRef: {name: registry, key: password}}
`

// deliveredInOrder are the restarts of cp1's delivery of the ClusterRegistry, in order.
var deliveredInOrder = regexp.MustCompile(`(?s)restarted chalkos-registry-mirrors\.service\n.*restarted chalkos-registry\.service\n.*restarted kubelet\.service\n`)

// pushedRE is the line of chalkctl registry push that names the pushed image's digest.
var pushedRE = regexp.MustCompile(`(?m)^pushing \S+ as \S+@(sha256:[0-9a-f]{64})$`)

// freedRE is the line of chalkctl registry gc of an endpoint that freed bytes.
var freedRE = regexp.MustCompile(`(?m)^192\.168\.100\.11: freed [0-9.]+ [KMG]iB$`)

// clusterRegistry gives cp1 and w1 a ClusterRegistry served by cp1, pushes busybox to it from an
// OCI archive through cp1's forwarded port, lists it as alike on every endpoint and in cp1's
// catalog, and runs it in a pod on w1, while the registry logs no request it served. It then
// deletes the image and collects the garbage, which frees its layers. It needs the offline
// images, which the online mode does not serve.
func clusterRegistry(t *testing.T, ctx context.Context, cs kubernetes.Interface, nodes map[string]*node, port int, online bool) {
	t.Helper()
	if online {
		t.Skip("the cluster registry's phase pushes the offline images")
	}
	group := filepath.Join(filepath.Dir(inventoryPath("base")), "groups", "registries.yaml")
	data, err := os.ReadFile(group)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(group, append(data, clusterRegistryDocs...), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := chalkctlContext(ctx, t, nil, "base", "config", "apply", "cp1", "w1",
		"--address", "cp1="+nodes["cp1"].addr, "--address", "w1="+nodes["w1"].addr)
	if err != nil {
		t.Fatal(err)
	}
	// On cp1 the kubelet restarts after containerd's hosts and the registry, which is ready once
	// it listens.
	cp1, _, _ := strings.Cut(out, "w1:")
	if !deliveredInOrder.MatchString(cp1) {
		t.Errorf("cp1's delivery does not restart containerd's hosts, the registry and the kubelet in that order:\n%s", cp1)
	}

	connect := "192.168.100.11=127.0.0.1:" + strconv.Itoa(port)
	out, err = chalkctlContext(ctx, t, nil, "base", "registry", "push", ociArchive(t, busyboxLayout(t)), clusterRegistryImage, "--connect-to", connect)
	if err != nil || !strings.Contains(out, "192.168.100.11: pushed 192.168.100.11:5000/"+clusterRegistryImage) {
		t.Fatalf("registry push: %v\n%s", err, out)
	}
	m := pushedRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("registry push names no digest:\n%s", out)
	}
	digest := m[1]
	if out, err := chalkctlContext(ctx, t, nil, "base", "registry", "list", "--connect-to", connect); err != nil || !regexp.MustCompile(`(?m)^`+regexp.QuoteMeta(clusterRegistryRepo)+` +1\.37\.0 +`+digest[:19]+` +all$`).MatchString(out) {
		t.Errorf("registry list: %v\n%s", err, out)
	}
	if out, err := chalkctl(t, nodes["cp1"], "base", "node", "diag", "cp1", "cluster-registry"); err != nil || !strings.Contains(out, clusterRegistryRepo) {
		t.Errorf("the catalog of cp1's cluster registry: %v\n%s", err, out)
	}
	podSucceeds(t, ctx, cs, "cluster-registry", clusterRegistryImage)
	// distribution logs each request it completed at info, which chalkos-registry leaves out.
	if out, err := chalkctl(t, nodes["cp1"], "base", "logs", "cp1", "--unit", "chalkos-registry.service"); err != nil || strings.Contains(out, "response completed") {
		t.Errorf("the registry's journal logs requests, or cannot be read: %v\n%s", err, out)
	}

	ref := clusterRegistryRepo + "@" + digest
	if out, err := chalkctlContext(ctx, t, nil, "base", "registry", "delete", ref, "--with-tags", "--yes", "--connect-to", connect); err != nil || !strings.Contains(out, "192.168.100.11: deleted 192.168.100.11:5000/"+ref+" and its tags 1.37.0") {
		t.Errorf("registry delete: %v\n%s", err, out)
	}
	if out, err := chalkctlContext(ctx, t, nil, "base", "registry", "gc", "--connect-to", connect); err != nil || !freedRE.MatchString(out) {
		t.Errorf("registry gc: %v\n%s", err, out)
	}
	if out, err := chalkctlContext(ctx, t, nil, "base", "registry", "list", "--connect-to", connect); err != nil || strings.Contains(out, clusterRegistryRepo) {
		t.Errorf("registry list after the delete: %v\n%s", err, out)
	}
}

// busyboxLayout is the OCI image layout of busybox that CHALKLAB_K8S_IMAGES lists.
func busyboxLayout(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("CHALKLAB_K8S_IMAGES"))
	if err != nil {
		t.Fatal(err)
	}
	var images map[string]string
	if err := json.Unmarshal(data, &images); err != nil {
		t.Fatal(err)
	}
	dir, ok := images["docker.io/library/busybox:1.37.0"]
	if !ok {
		t.Fatal("CHALKLAB_K8S_IMAGES holds no busybox")
	}
	return dir
}

// ociArchive writes the OCI image layout dir as an OCI archive, a tar of the layout.
func ociArchive(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "busybox.tar")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	if err := tw.AddFS(os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
