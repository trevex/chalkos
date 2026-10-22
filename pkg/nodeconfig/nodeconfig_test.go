package nodeconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/store"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/pki"
)

const nodeIdentity = `{
  "hostname": "w1",
  "network": {},
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
		Services:    filepath.Join(root, "etc", "services.json"),
		Effects:     filepath.Join(root, "etc", "effects.json"),
		Slots:       filepath.Join(root, "state", "config"),
		Cmdline:     filepath.Join(root, "cmdline"),
		Schemas:     filepath.Join(root, "etc", "schemas"),
		SetHostname: func(name string) error { hostname = name; return nil },
	}
	write(t, l.Services, "{}")
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
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(l.RunDir, "node.json")); got != nodeIdentity {
		t.Errorf("node.json = %s", got)
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
	write(t, l.Services, `{
	  "demo": {"kinds": ["Demo"]},
	  "idle": {"kinds": ["Host"]},
	  "reloaded": {"kinds": []}
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
	write(t, l.Services, `{"demo": {"kinds": ["Demo"]}}`)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(filepath.Join(l.RunDir, "credentials", "demo")); err != nil || len(entries) != 0 {
		t.Errorf("demo's credentials = %v, %v; want an empty directory", entries, err)
	}
}

// ReadEffects reads each effect's triggers and actions.
func TestReadEffects(t *testing.T) {
	l, _ := newTestLoader(t)
	write(t, l.Effects, `{"networkd": {"when": "facts.units != oldFacts.units", "reload": ["systemd-networkd.service"]},
	  "kubelet": {"outputs": ["cluster"], "restart": ["kubelet.service"], "start": true, "chalkd": "restart"},
	  "rack": {"kinds": ["Rack"], "restart": ["rack.service"]}}`)
	got, err := ReadEffects(l.Effects)
	want := map[string]Effect{
		"networkd": {When: "facts.units != oldFacts.units", Reload: []string{"systemd-networkd.service"}},
		"kubelet":  {Outputs: []string{"cluster"}, Restart: []string{"kubelet.service"}, Start: true, Chalkd: ChalkdRestart},
		"rack":     {Kinds: []string{"Rack"}, Restart: []string{"rack.service"}},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("effects = %+v, %v; want %+v", got, err, want)
	}
	if got, err := ReadEffects(filepath.Join(t.TempDir(), "missing.json")); err != nil || len(got) != 0 {
		t.Errorf("a missing file: %v, %v", got, err)
	}
}

// ReadEffects refuses an effect chalkd could not carry out: one nothing triggers, one that does
// nothing, a unit that is no unit name, a chalkd action it does not know and a command that names
// no program it can run, since chalkd splits the command on spaces and runs the first word, from
// its PATH or the image's store.
func TestReadEffectsRefuses(t *testing.T) {
	for name, tc := range map[string]struct{ effect, want string }{
		"no trigger":             {`{"reload": ["a.service"]}`, "effect e: it has none of outputs, kinds and when"},
		"no action":              {`{"when": "true"}`, "effect e: it restarts, reloads and runs nothing"},
		"a unit without suffix":  {`{"when": "true", "restart": ["kubelet"]}`, `"kubelet" is no unit name`},
		"a chalkd action":        {`{"when": "true", "chalkd": "stop"}`, `chalkd is "stop", not restart or reload`},
		"spaces":                 {`{"when": "true", "run": "  "}`, "effect e: the command to run is empty"},
		"outside the store":      {`{"when": "true", "run": "/usr/bin/chronyc reload sources"}`, `the command "/usr/bin/chronyc reload sources" names /usr/bin/chronyc, which is neither`},
		"a relative path":        {`{"when": "true", "run": "bin/chronyc reload"}`, "names bin/chronyc, which is neither"},
		"a bare program":         {`{"when": "true", "run": "chronyc reload sources"}`, ""},
		"a program of the store": {`{"outputs": ["x"], "run": "/nix/store/x-chrony/bin/chronyc reload sources"}`, ""},
		"chalkd alone":           {`{"when": "true", "chalkd": "reload"}`, ""},
		"kinds alone":            {`{"kinds": ["Rack"], "restart": ["a.service"]}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			l, _ := newTestLoader(t)
			write(t, l.Effects, `{"e": `+tc.effect+`}`)
			_, err := ReadEffects(l.Effects)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestApplyWritesNoKeyCredentials(t *testing.T) {
	l, _ := newTestLoader(t)
	write(t, l.Services, `{"report": {"keys": ["hostname"]}}`)
	if err := l.Apply([]byte(nodeIdentity)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.RunDir, "credentials", "hostname")); !os.IsNotExist(err) {
		t.Errorf("wrote the credential of an identity key: %v", err)
	}
}

// kubernetesConfig is a worker's set with its Kubernetes and Node documents as chalkd stores them,
// schema defaults filled in.
const kubernetesConfig = `
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Cluster
metadata: {name: default}
spec:
  endpoint: "https://10.0.0.10:6443"
  domain: cluster.local
  allowSchedulingOnControlPlanes: false
  ipFamilies: [ipv4]
  podCIDRs: {ipv4: 10.244.0.0/16, ipv6: "fd00:10:244::/56"}
  serviceCIDRs: {ipv4: 10.96.0.0/12, ipv6: "fd00:10:96::/112"}
  dnsIPs: {ipv4: null, ipv6: null}
  nodeCIDRMaskSizes: {ipv4: 24, ipv6: 64}
  nodeIP: {validSubnets: [], timeout: 300}
  vip: {addresses: [], mode: l2}
  extraArgs: {etcd: {}, kube-apiserver: {}, kube-controller-manager: {}, kube-scheduler: {}}
---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Node
metadata: {name: default}
spec:
  type: worker
  labels: {}
  taints: []
  kubeletExtraArgs: {}
  nodeIP: {addresses: null, validSubnets: null}
  vip: {interface: null}
`

const kubernetesImage = `{"version": "1.37.1", "cni": "none", "images": {"etcd": "e", "kubeAPIServer": "a",
  "kubeControllerManager": "c", "kubeScheduler": "s", "kubeProxy": "p", "coredns": "d", "flannel": ""}}`

// kubernetesLoader is a loader on an image with Kubernetes whose STATE holds the documents.
func kubernetesLoader(t *testing.T, documents string) Loader {
	t.Helper()
	l, _ := newTestLoader(t)
	copySchemas(t, l.Schemas)
	root := filepath.Dir(filepath.Dir(l.Services))
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
	// Another cluster replaces it.
	c.Domain = "lab.local"
	if err := l.WriteCluster(&c); err != nil {
		t.Fatal(err)
	}
	if got, err := kubernetes.ReadCluster(l.ClusterFile); err != nil || got.Domain != "lab.local" {
		t.Errorf("new domain: %+v, %v", got, err)
	}
}

// A set without a Cluster document leaves no cluster file, and says why.
func TestLoadWithoutClusterDocument(t *testing.T) {
	l := kubernetesLoader(t, "apiVersion: kubernetes.chalkos.dev"+strings.SplitN(kubernetesConfig, "---\napiVersion: kubernetes.chalkos.dev", 2)[1])
	write(t, l.ClusterFile, "{}")
	if err := l.Load(); err == nil || !strings.Contains(err.Error(), "no Cluster") {
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

// outputLoader is a loader whose image renders the output demo from the Demo documents with the
// shell commands given, for the node its certificate on STATE names, w1.
func outputLoader(t *testing.T, render string) Loader {
	t.Helper()
	l, _ := newTestLoader(t)
	copySchemas(t, l.Schemas)
	root := filepath.Dir(filepath.Dir(l.Services))
	l.Render = outputs.Renderer{Root: filepath.Join(l.RunDir, "outputs"), Run: outputs.Direct{}}
	l.Outputs = filepath.Join(root, "etc", "outputs.json")
	l.NodeCertificate = filepath.Join(root, "state", "chalkd", "node.pem")
	cert, err := pki.NewCA("w1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	write(t, l.NodeCertificate, cert.Key+cert.Certificate)
	script := filepath.Join(t.TempDir(), "render")
	write(t, script, "#!/bin/sh\nset -e\n"+render+"\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	defs, _ := json.Marshal(map[string]outputs.Definition{"demo": {
		Inputs: []string{"Demo"}, Render: script, Dir: filepath.Join(l.RunDir, "outputs", "demo"),
		User: strconv.Itoa(os.Getuid()), Group: strconv.Itoa(os.Getgid()), Mode: "0644",
	}})
	write(t, l.Outputs, string(defs))
	writeSlot(t, l, running, nodeIdentity, loaderConfig)
	return l
}

// Load renders every output of the running slot's documents and swaps it in, as /run starts
// empty at boot, for the node its certificate names.
func TestLoadRendersOutputs(t *testing.T) {
	l := outputLoader(t, `cat > "$OUT/demo.json"`)
	if err := l.Load(); err != nil {
		t.Fatal(err)
	}
	got := read(t, filepath.Join(l.RunDir, "outputs", "demo", "demo.json"))
	if !strings.Contains(got, `"name":"w1"`) || !strings.Contains(got, `"name":"first"`) || strings.Contains(got, "s3cret") {
		t.Errorf("the output = %s", got)
	}
	st, err := outputs.ReadStatus(l.Render.Root)
	if err != nil || st["demo"].Hash == "" || st["demo"].Error != "" {
		t.Errorf("status = %+v, %v", st, err)
	}
}

// A renderer that fails at boot fails the load, which fails chalkos-config and so the boot's
// health check, after the rest of the config is applied; the status names the output.
func TestLoadFailsForAFailingOutput(t *testing.T) {
	l := outputLoader(t, `exit 2`)
	err := l.Load()
	if err == nil || !strings.Contains(err.Error(), "output demo: its renderer failed (exit 2)") {
		t.Fatalf("err = %v, want the output named", err)
	}
	if got := read(t, filepath.Join(l.RunDir, "node.json")); got != nodeIdentity {
		t.Errorf("node.json = %s, want the identity applied all the same", got)
	}
	if st, _ := outputs.ReadStatus(l.Render.Root); st["demo"].Error != "its renderer failed (exit 2); see the node's journal" {
		t.Errorf("status = %+v", st)
	}
}

// An image with outputs renders them for the node its certificate names, so a boot whose
// certificate cannot be read fails the load and renders nothing for a node without a name.
func TestLoadFailsWithoutTheNodesName(t *testing.T) {
	l := outputLoader(t, `cat > "$OUT/demo.json"`)
	if err := os.Remove(l.NodeCertificate); err != nil {
		t.Fatal(err)
	}
	if err := l.Load(); err == nil || !strings.Contains(err.Error(), "the node's name for its outputs") {
		t.Fatalf("err = %v, want the node's name to fail", err)
	}
	if _, err := os.Stat(filepath.Join(l.RunDir, "outputs", "demo")); !os.IsNotExist(err) {
		t.Errorf("rendered without the node's name: %v", err)
	}
}
