package nodeconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/store"
	"github.com/trevex/chalkos/pkg/kubernetes"
)

const nodeIdentity = `{
  "hostname": "w1",
  "network": {},
  "networkUnits": {"10-uplink.network": "[Match]\nName=enp1s0\n"},
  "labels": {"zone": "a"},
  "taints": [],
  "storage": {}
}`

func newTestLoader(t *testing.T) (Loader, *string) {
	t.Helper()
	root := t.TempDir()
	hostname := ""
	l := Loader{
		RunDir:      filepath.Join(root, "run", "chalkos"),
		NetworkDir:  filepath.Join(root, "run", "systemd", "network"),
		Consumers:   filepath.Join(root, "etc", "consumers.json"),
		Slots:       filepath.Join(root, "state", "config"),
		Cmdline:     filepath.Join(root, "cmdline"),
		Schemas:     filepath.Join(root, "etc", "schemas"),
		SetHostname: func(name string) error { hostname = name; return nil },
	}
	write(t, l.Consumers, "{}")
	write(t, l.Cmdline, "init=/x usrhash="+running+"\n")
	return l, &hostname
}

// running is the root hash of the image the test loader runs, as its kernel command line names it.
var running = strings.Repeat("ab", 32)

// writeSlot writes the slot of the image with the root hash, in hex, holding the identity and the
// documents, as delivered and as their defaults leave them.
func writeSlot(t *testing.T, l Loader, hash, identity, documents string) {
	t.Helper()
	docs, err := config.Parse(strings.NewReader(documents))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Write(filepath.Join(l.Slots, hash), store.Stored{Delivered: docs, Documents: docs, Version: "v1", Identity: []byte(identity)}); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestLoad(t *testing.T) {
	l, hostname := newTestLoader(t)
	writeSlot(t, l, running, nodeIdentity, "")
	// A unit from an earlier identity, and one an administrator placed there.
	write(t, filepath.Join(l.NetworkDir, "05-old.network"), Header+"[Match]\nName=eth9\n")
	write(t, filepath.Join(l.NetworkDir, "90-manual.network"), "[Match]\nName=eth8\n")

	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(l.RunDir, "node.json")); got != nodeIdentity {
		t.Errorf("node.json = %s", got)
	}
	if got := read(t, filepath.Join(l.NetworkDir, "10-uplink.network")); got != Header+"[Match]\nName=enp1s0\n" {
		t.Errorf("uplink unit = %q", got)
	}
	if _, err := os.Stat(filepath.Join(l.NetworkDir, "05-old.network")); !os.IsNotExist(err) {
		t.Error("kept a unit of an earlier identity")
	}
	if _, err := os.Stat(filepath.Join(l.NetworkDir, "90-manual.network")); err != nil {
		t.Error("removed a unit the loader did not write")
	}
	if *hostname != "w1" {
		t.Errorf("hostname = %q", *hostname)
	}
}

// TestLoadWithoutSlots applies nothing on a node without config slots, which is not installed, such
// as the installer, whose STATE is not mounted: it reads no kernel command line either.
func TestLoadWithoutSlots(t *testing.T) {
	l, hostname := newTestLoader(t)
	if err := os.Remove(l.Cmdline); err != nil {
		t.Fatal(err)
	}
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.RunDir, "node.json")); !os.IsNotExist(err) || *hostname != "" {
		t.Error("applied an identity of a node that has none")
	}
}

// TestLoadSelectsTheRunningImagesSlot applies the slot the kernel command line's usrhash= names,
// not another image's.
func TestLoadSelectsTheRunningImagesSlot(t *testing.T) {
	l, hostname := newTestLoader(t)
	writeSlot(t, l, strings.Repeat("cd", 32), strings.Replace(nodeIdentity, "w1", "other", 1), "")
	writeSlot(t, l, running, nodeIdentity, "")
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if *hostname != "w1" {
		t.Errorf("hostname = %q, want the running image's w1", *hostname)
	}
	if slot, err := l.Slot(); err != nil || slot != filepath.Join(l.Slots, running) {
		t.Errorf("slot = %s, %v", slot, err)
	}
}

// TestLoadWithoutTheRunningImagesSlot fails on a node whose slots hold another image's config
// alone, and applies none of it: the boot runs without config, so boot counting falls back to an
// image that has its own.
func TestLoadWithoutTheRunningImagesSlot(t *testing.T) {
	l, hostname := newTestLoader(t)
	writeSlot(t, l, strings.Repeat("cd", 32), nodeIdentity, "")
	if err := l.Load(); err == nil || !strings.Contains(err.Error(), "no config for the running image "+running) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(l.RunDir, "node.json")); !os.IsNotExist(err) || *hostname != "" {
		t.Error("applied another image's identity")
	}
}

func TestApplyRejectsUnitNames(t *testing.T) {
	l, _ := newTestLoader(t)
	for _, name := range []string{"../escape.network", "10-uplink.service"} {
		err := l.Apply([]byte(`{"networkUnits": {"` + name + `": ""}}`))
		if err == nil || !strings.Contains(err.Error(), "invalid networkd unit name") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestApplyWritesTimeServers(t *testing.T) {
	l, _ := newTestLoader(t)
	l.ChronySources = filepath.Join(l.RunDir, "chrony", "identity.sources")
	id := `{"hostname": "n1", "time": {"servers": [{"host": "ptbtime1.ptb.de", "nts": true}, {"host": "10.0.2.2", "nts": false, "port": 12300}]}}`
	if err := l.Apply([]byte(id)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(l.ChronySources)
	if err != nil {
		t.Fatal(err)
	}
	if want := "server ptbtime1.ptb.de iburst nts\nserver 10.0.2.2 iburst port 12300\n"; string(got) != want {
		t.Errorf("sources = %q, want %q", got, want)
	}
	// An identity without servers leaves chrony none.
	if err := l.Apply([]byte(`{"hostname": "n1"}`)); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(l.ChronySources); len(got) != 0 {
		t.Errorf("sources = %q, want none", got)
	}
	for _, host := range []string{"a b", "x\nserver evil.example iburst", "-x", ""} {
		bad, _ := json.Marshal(map[string]any{"time": map[string]any{"servers": []any{map[string]any{"host": host}}}})
		if err := l.Apply(bad); err == nil {
			t.Errorf("host %q: applied", host)
		}
	}
}

const loaderConfig = `
apiVersion: demo.example.org/v1alpha1
kind: Demo
metadata: {name: one}
spec: {name: first, token: {secretRef: {name: demo, key: token}}}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: demo}
spec: {data: {token: s3cret}}
`

// copySchemas copies the fixture schemas of the built-in kinds and the Demo kind into dir.
func copySchemas(t *testing.T, dir string) {
	t.Helper()
	for _, src := range []string{"../../test/fixtures/kinds/builtin", "../../test/fixtures/kinds/image/schemas"} {
		entries, err := os.ReadDir(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			write(t, filepath.Join(dir, e.Name()), read(t, filepath.Join(src, e.Name())))
		}
	}
}

func TestLoadWritesConfig(t *testing.T) {
	l, _ := newTestLoader(t)
	copySchemas(t, l.Schemas)
	write(t, l.Consumers, `{
	  "demo": {"kinds": ["Demo"], "restart": {"when": "\"Demo\" in changedKinds"}},
	  "idle": {"kinds": ["Cluster"], "restart": {"when": "\"Cluster\" in changedKinds"}},
	  "reloaded": {"kinds": [], "reload": {"when": "true", "command": "systemctl reload reloaded"}}
	}`)
	writeSlot(t, l, running, nodeIdentity, loaderConfig)

	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(l.RunDir, "node.json")); got != nodeIdentity {
		t.Errorf("node.json = %s", got)
	}
	if got := read(t, filepath.Join(l.RunDir, "config", "demo.example.org", "Demo", "one.json")); !strings.Contains(got, `"name":"first"`) {
		t.Errorf("Demo = %s", got)
	}
	if _, err := os.Stat(filepath.Join(l.RunDir, "config", "chalkos.dev", "Secret")); !os.IsNotExist(err) {
		t.Error("wrote the Secret below /run/chalkos/config")
	}
	if got := read(t, filepath.Join(l.RunDir, "credentials", "demo", "demo.token")); got != "s3cret" {
		t.Errorf("credential = %q", got)
	}
	if entries, err := os.ReadDir(filepath.Join(l.RunDir, "credentials", "idle")); err != nil || len(entries) != 0 {
		t.Errorf("idle's credentials = %v, %v; want an empty directory", entries, err)
	}
	// A unit of no kinds loads no credentials.
	if _, err := os.Stat(filepath.Join(l.RunDir, "credentials", "reloaded")); !os.IsNotExist(err) {
		t.Errorf("reloaded's credentials: %v; want none", err)
	}
}

func TestLoadWithoutConfigMakesCredentialDirectories(t *testing.T) {
	l, _ := newTestLoader(t)
	writeSlot(t, l, running, nodeIdentity, "")
	write(t, l.Consumers, `{"demo": {"kinds": ["Demo"], "restart": {"when": "\"Demo\" in changedKinds"}}}`)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(filepath.Join(l.RunDir, "credentials", "demo")); err != nil || len(entries) != 0 {
		t.Errorf("demo's credentials = %v, %v; want an empty directory", entries, err)
	}
}

// ReadConsumers reads each consumer's kinds and rules.
func TestReadConsumers(t *testing.T) {
	l, _ := newTestLoader(t)
	write(t, l.Consumers, `{"networkd": {"kinds": [], "restart": {"when": "false", "start": false},
	  "reload": {"when": "\"Network\" in changedKinds", "command": "networkctl reload"}}}`)
	got, err := ReadConsumers(l.Consumers)
	want := map[string]Consumer{"networkd": {
		Kinds:   []string{},
		Restart: Restart{When: "false"},
		Reload:  Reload{When: `"Network" in changedKinds`, Command: "networkctl reload"},
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("consumers = %+v, %v; want %+v", got, err, want)
	}
}

func TestApplyWritesNoKeyCredentials(t *testing.T) {
	l, _ := newTestLoader(t)
	write(t, l.Consumers, `{"report": {"keys": ["hostname"], "restart": {"when": "true"}}}`)
	if err := l.Apply([]byte(nodeIdentity)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.RunDir, "credentials", "hostname")); !os.IsNotExist(err) {
		t.Errorf("wrote the credential of an identity key: %v", err)
	}
}

// kubernetesConfig is a worker's set with its Kubernetes document as chalkd stores it, schema
// defaults filled in.
const kubernetesConfig = `
apiVersion: chalkos.dev/v1alpha1
kind: Cluster
metadata: {name: lab}
spec: {endpoint: "https://10.0.0.10:6443"}
---
apiVersion: chalkos.dev/v1alpha1
kind: Node
metadata: {name: w1}
spec: {type: worker, imageRef: metal, hostname: null}
---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Kubernetes
metadata: {name: kubernetes}
spec:
  domain: cluster.local
  allowSchedulingOnControlPlanes: false
  ipFamilies: [ipv4]
  podCIDRs: {ipv4: 10.244.0.0/16, ipv6: "fd00:10:244::/56"}
  serviceCIDRs: {ipv4: 10.96.0.0/12, ipv6: "fd00:10:96::/112"}
  dnsIPs: {ipv4: null, ipv6: null}
  nodeCIDRMaskSizes: {ipv4: 24, ipv6: 64}
  nodeIP: {validSubnets: [], timeout: 300}
  vxlanSourceSubnets: []
  vip: {addresses: [], mode: l2, interface: null}
  extraArgs: {etcd: {}, kube-apiserver: {}, kube-controller-manager: {}, kube-scheduler: {}, kubelet: {}}
`

const kubernetesImage = `{"version": "1.37.1", "cni": "none", "images": {"etcd": "e", "kubeAPIServer": "a",
  "kubeControllerManager": "c", "kubeScheduler": "s", "kubeProxy": "p", "coredns": "d", "flannel": ""}}`

// kubernetesLoader is a loader on an image with Kubernetes whose STATE holds the documents.
func kubernetesLoader(t *testing.T, documents string) Loader {
	t.Helper()
	l, _ := newTestLoader(t)
	copySchemas(t, l.Schemas)
	root := filepath.Dir(filepath.Dir(l.Consumers))
	l.KubernetesImage = filepath.Join(root, "etc", "kubernetes", "image.json")
	l.ClusterFile = filepath.Join(root, "run", "chalkos", "kubernetes", "cluster.json")
	write(t, l.KubernetesImage, kubernetesImage)
	writeSlot(t, l, running, nodeIdentity, documents)
	return l
}

func TestLoadWritesTheCluster(t *testing.T) {
	l := kubernetesLoader(t, kubernetesConfig)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	c, err := kubernetes.ReadCluster(l.ClusterFile)
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != kubernetes.KindWorker || c.Endpoint != "https://10.0.0.10:6443" || c.DNSIPs.IPv4 != "10.96.0.10" || c.Images.KubeProxy != "p" {
		t.Errorf("cluster = %+v", c)
	}
	// The same cluster changes nothing; another does.
	if changed, err := l.WriteCluster(&c); err != nil || changed {
		t.Errorf("rewrite: changed = %v, %v", changed, err)
	}
	c.Domain = "lab.local"
	if changed, err := l.WriteCluster(&c); err != nil || !changed {
		t.Errorf("new domain: changed = %v, %v", changed, err)
	}
}

// A set without a Kubernetes document leaves no cluster file, and says why.
func TestLoadWithoutKubernetesDocument(t *testing.T) {
	l := kubernetesLoader(t, strings.SplitN(kubernetesConfig, "---\napiVersion: kubernetes.chalkos.dev", 2)[0])
	write(t, l.ClusterFile, "{}")
	if err := l.Load(); err == nil || !strings.Contains(err.Error(), "no Kubernetes") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(l.ClusterFile); !os.IsNotExist(err) {
		t.Errorf("kept the cluster file: %v", err)
	}
}

// A slot without documents, an identity alone, has no cluster file; an image without Kubernetes
// neither.
func TestLoadWithoutSetOrKubernetesRemovesTheCluster(t *testing.T) {
	l := kubernetesLoader(t, "")
	write(t, l.ClusterFile, "{}")
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.ClusterFile); !os.IsNotExist(err) {
		t.Errorf("without a set: kept the cluster file: %v", err)
	}

	l = kubernetesLoader(t, kubernetesConfig)
	if err := os.Remove(l.KubernetesImage); err != nil {
		t.Fatal(err)
	}
	write(t, l.ClusterFile, "{}")
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.ClusterFile); !os.IsNotExist(err) {
		t.Errorf("without Kubernetes: kept the cluster file: %v", err)
	}
}
