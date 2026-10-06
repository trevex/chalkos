package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
)

func TestRunSignRequiresAllFlags(t *testing.T) {
	err := runSign([]string{"--image", "disk.raw"})
	if err == nil || !strings.Contains(err.Error(), "--repart-json") {
		t.Fatalf("err = %v, want a missing --repart-json error", err)
	}
}

const testManifest = `{
  "schemaVersion": 0,
  "cluster": {"name": "lab", "endpoint": "https://10.0.0.10:6443"},
  "roles": {"test": {"image": "roles.test.image"}},
  "nodes": {
    "n1": {
      "role": "test",
      "identity": {
        "hostname": "n1",
        "network": {"networks": {"10-uplink": {"address": ["10.0.0.11/24"]}}},
        "networkUnits": {},
        "labels": {},
        "taints": [],
        "storage": {
          "disks": {"system": {"ref": {"serial": "chalk-target"}, "seed": "2869f04c-5655-50f4-28b9-6b2eb9700a02", "repart": {"50-var.conf": "[Partition]\nLabel=var\n"}}},
          "volumes": {"var": {"disk": "system", "label": "var", "format": "ext4", "mountPoint": "/var", "encryption": "tpm2", "size": null}},
          "fallback": "recovery-key",
          "encryption": "tpm2"
        },
        "extensions": {}
      }
    }
  }
}`

// testApp is chalkctl with a manifest and a plaintext secrets file in a temporary directory.
type testApp struct {
	*app
	dir      string
	secrets  pki.Secrets
	stdout   *bytes.Buffer
	stderr   *bytes.Buffer
	manifest *manifest.Manifest
}

func newTestApp(t *testing.T) *testApp {
	t.Helper()
	dir := t.TempDir()
	secrets, err := pki.GenerateSecrets(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := secrets.Encode()
	writeFile(t, filepath.Join(dir, "secrets.json"), string(data))
	writeFile(t, filepath.Join(dir, "manifest.json"), testManifest)
	m, err := manifest.Decode(strings.NewReader(testManifest))
	if err != nil {
		t.Fatal(err)
	}
	ta := &testApp{dir: dir, secrets: secrets, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, manifest: m}
	ta.app = &app{
		stdin:  strings.NewReader(""),
		stdout: ta.stdout,
		stderr: ta.stderr,
		nix:    func(context.Context, ...string) ([]byte, error) { return nil, errors.New("nix is not available") },
		home:   dir,
	}
	return ta
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// args prefixes the flags that point chalkctl at the test cluster and the node's address.
func (ta *testApp) args(cmd []string, addr string) []string {
	return append(cmd, "--manifest", filepath.Join(ta.dir, "manifest.json"), "--flake", ta.dir, "--endpoint", addr)
}

// startNode serves a chalkd in the mode with the test cluster's OS CA. In normal mode it serves
// a node certificate for n1.
func (ta *testApp) startNode(t *testing.T, s *chalkd.Server) string {
	t.Helper()
	cert := ta.secrets.OSCA
	var err error
	if s.Mode == nodev1.Mode_MODE_NORMAL {
		cert, err = pki.IssueNode(ta.secrets.OSCA, "n1", []string{"n1"}, nil, time.Now())
	} else {
		cert, err = pki.SelfSigned("chalkd", time.Now())
	}
	if err != nil {
		t.Fatal(err)
	}
	pair, _ := tls.X509KeyPair([]byte(cert.Certificate), []byte(cert.Key))
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	s.Fingerprint = pki.Fingerprint(leaf.Raw)
	ca, _ := pki.ParseCertificate([]byte(ta.secrets.OSCA.Certificate))
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	srv := httptest.NewUnstartedServer(s.Handler())
	srv.EnableHTTP2 = true
	srv.TLS = chalkd.TLSConfig(pair, pool)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

func maintenanceNode() *chalkd.Server {
	return &chalkd.Server{
		Mode:       nodev1.Mode_MODE_MAINTENANCE,
		InPlace:    func(context.Context, install.Request) error { return errors.New("unexpected install in place") },
		FromMedia:  func(context.Context, install.MediaRequest) error { return errors.New("unexpected install from media") },
		RebootNode: func() {},
	}
}

func TestGenSecretsPlaintext(t *testing.T) {
	ta := newTestApp(t)
	out := filepath.Join(ta.dir, "new")
	os.Mkdir(out, 0o755)
	if err := ta.run(context.Background(), []string{"gen", "secrets", "--plaintext", "--out", out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stderr.String(), ".gitignore") {
		t.Errorf("no warning about the unencrypted file: %q", ta.stderr)
	}
	data, err := os.ReadFile(filepath.Join(out, "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pki.ReadSecrets(data, nil); err != nil {
		t.Errorf("the secrets file does not read back: %v", err)
	}
	if info, _ := os.Stat(filepath.Join(out, "secrets.json")); info.Mode().Perm() != 0o600 {
		t.Errorf("secrets.json mode = %v", info.Mode())
	}
	var pub pki.Public
	pubData, _ := os.ReadFile(filepath.Join(out, "secrets.pub.json"))
	if err := json.Unmarshal(pubData, &pub); err != nil || pub.OSCA.Certificate == "" || pub.OSCA.Key != "" || bytes.Contains(pubData, []byte("PRIVATE")) {
		t.Errorf("secrets.pub.json = %s, %v", pubData, err)
	}
	err = ta.run(context.Background(), []string{"gen", "secrets", "--plaintext", "--out", out})
	if err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("err = %v, want a refusal to overwrite the secrets", err)
	}
	if err := ta.run(context.Background(), []string{"gen", "secrets", "--out", out}); err == nil {
		t.Error("generated secrets without recipients or --plaintext")
	}
}

func TestGenSecretsAgeAndRecoveryKey(t *testing.T) {
	ta := newTestApp(t)
	id, _ := age.GenerateX25519Identity()
	writeFile(t, filepath.Join(ta.dir, ".config", "chalkos", "age.key"), id.String()+"\n")
	out := filepath.Join(ta.dir, "cluster")
	os.Mkdir(out, 0o755)
	if err := ta.run(context.Background(), []string{"gen", "secrets", "--recipient", id.Recipient().String(), "--out", out}); err != nil {
		t.Fatal(err)
	}
	encrypted, err := os.ReadFile(filepath.Join(out, "secrets.age"))
	if err != nil || !bytes.HasPrefix(encrypted, []byte("age-encryption.org/")) {
		t.Fatalf("secrets.age = %.20q, %v", encrypted, err)
	}

	// The default secrets file next to the flake, decrypted with the default identity.
	writeFile(t, filepath.Join(out, "manifest.json"), testManifest)
	ta.stdout.Reset()
	if err := ta.run(context.Background(), []string{"recovery-key", "n1", "--flake", out, "--manifest", filepath.Join(out, "manifest.json")}); err != nil {
		t.Fatal(err)
	}
	secrets, _ := pki.ReadSecrets(encrypted, func() ([]age.Identity, error) { return []age.Identity{id}, nil })
	want, _ := pki.RecoveryKey(secrets.RecoverySecret, "lab", "n1")
	if got := strings.TrimSpace(ta.stdout.String()); got != want {
		t.Errorf("recovery key = %q, want %q", got, want)
	}

	// The same file from standard input, as a secret manager would supply it.
	ta.stdout.Reset()
	ta.stdin = bytes.NewReader(encrypted)
	if err := ta.run(context.Background(), []string{"recovery-key", "n1", "--secrets", "-", "--manifest", filepath.Join(out, "manifest.json")}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(ta.stdout.String()); got != want {
		t.Errorf("recovery key from stdin = %q, want %q", got, want)
	}
}

func TestInstallInPlace(t *testing.T) {
	ta := newTestApp(t)
	s := maintenanceNode()
	var got install.Request
	s.InPlace = func(_ context.Context, req install.Request) error { got = req; return nil }
	addr := ta.startNode(t, s)

	if err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--insecure"}, addr)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stderr.String(), "fingerprint is "+s.Fingerprint) {
		t.Errorf("stderr = %q, want the observed fingerprint", ta.stderr)
	}
	if !strings.Contains(ta.stdout.String(), "n1 is installed and reboots") {
		t.Errorf("stdout = %q", ta.stdout)
	}
	want, _ := json.Marshal(ta.manifest.Nodes["n1"].Identity)
	if !bytes.Equal(got.Identity, want) {
		t.Errorf("identity = %s, want %s", got.Identity, want)
	}
	key, _ := pki.RecoveryKey(ta.secrets.RecoverySecret, "lab", "n1")
	if got.FallbackSecret != key {
		t.Errorf("fallback secret = %q, want the node's recovery key", got.FallbackSecret)
	}
	cert, _, err := (pki.CertKey{Certificate: string(got.NodeCertificate), Key: string(got.NodeKey)}).Parse()
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := pki.ParseCertificate([]byte(ta.secrets.OSCA.Certificate))
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, DNSName: "n1"}); err != nil {
		t.Errorf("node certificate: %v", err)
	}
	if len(cert.IPAddresses) != 1 || !cert.IPAddresses[0].Equal(net.ParseIP("10.0.0.11")) {
		t.Errorf("node certificate addresses = %v, want the static address", cert.IPAddresses)
	}
	if string(got.CA) != ta.secrets.OSCA.Certificate {
		t.Error("the OS CA was not sent")
	}
}

func TestInstallRefusesWrongFingerprint(t *testing.T) {
	ta := newTestApp(t)
	s := maintenanceNode()
	called := false
	s.InPlace = func(context.Context, install.Request) error { called = true; return nil }
	addr := ta.startNode(t, s)
	err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--fingerprint", strings.Repeat("00", 32)}, addr))
	if err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("err = %v", err)
	}
	if called {
		t.Error("installed a node whose fingerprint did not match")
	}
	if err := ta.run(context.Background(), ta.args([]string{"install", "n1"}, addr)); err == nil || !strings.Contains(err.Error(), "--fingerprint") {
		t.Errorf("err = %v, want a demand for --fingerprint or --insecure", err)
	}
}

func TestInstallFromMediaStreamsImage(t *testing.T) {
	ta := newTestApp(t)
	image := bytes.Repeat([]byte("chalkos image "), 200000)
	imageDir := filepath.Join(ta.dir, "image")
	writeFile(t, filepath.Join(imageDir, "chalkos_0.1.0.raw"), string(image))
	writeFile(t, filepath.Join(imageDir, "repart.d", "50-state.conf"), "[Partition]\nLabel=state\n")
	s := maintenanceNode()
	s.Installer = true
	var got install.MediaRequest
	var streamed []byte
	s.FromMedia = func(_ context.Context, req install.MediaRequest) error {
		got = req
		var err error
		streamed, err = io.ReadAll(req.Image)
		return err
	}
	addr := ta.startNode(t, s)
	fp := s.Fingerprint

	if err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--fingerprint", fp, "--image", imageDir}, addr)); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(image)
	if !bytes.Equal(streamed, image) || got.ImageSize != int64(len(image)) || !bytes.Equal(got.ImageSHA256, sum[:]) {
		t.Errorf("streamed %d bytes, header size %d", len(streamed), got.ImageSize)
	}
	if got.Target != (storage.Ref{Selector: storage.Selector{Serial: "chalk-target"}}) || got.WipeDisk {
		t.Errorf("target = %+v, wipe = %v", got.Target, got.WipeDisk)
	}
	if !reflect.DeepEqual(got.SystemDefinitions, map[string]string{"50-state.conf": "[Partition]\nLabel=state\n"}) {
		t.Errorf("definitions = %v", got.SystemDefinitions)
	}
}

func TestInstallReportsNodeRefusal(t *testing.T) {
	ta := newTestApp(t)
	s := maintenanceNode()
	s.InPlace = func(context.Context, install.Request) error {
		return errors.New("the identity places the system on /dev/vdb, but the node runs from /dev/vda")
	}
	addr := ta.startNode(t, s)
	err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--insecure"}, addr))
	if err == nil || !strings.Contains(err.Error(), "runs from /dev/vda") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallFromMediaFailureNamesWipeDisk(t *testing.T) {
	ta := newTestApp(t)
	imageDir := filepath.Join(ta.dir, "image")
	writeFile(t, filepath.Join(imageDir, "chalkos_0.1.0.raw"), "chalkos image")
	writeFile(t, filepath.Join(imageDir, "repart.d", "50-state.conf"), "[Partition]\nLabel=state\n")
	s := maintenanceNode()
	s.Installer = true
	s.FromMedia = func(_ context.Context, req install.MediaRequest) error {
		io.Copy(io.Discard, req.Image)
		return errors.New("enroll the TPM2 keyslot of var: no TPM")
	}
	addr := ta.startNode(t, s)
	err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--insecure", "--image", imageDir}, addr))
	if err == nil || !strings.Contains(err.Error(), "no TPM") || !strings.Contains(err.Error(), "--wipe-disk") {
		t.Fatalf("err = %v, want the node's error and the need for --wipe-disk", err)
	}
}

func TestPasswordFileTrimsOneNewline(t *testing.T) {
	ta := newTestApp(t)
	node := ta.manifest.Nodes["n1"]
	node.Identity.Storage.Fallback = "password"
	tg := &target{cluster: &cluster{manifest: ta.manifest}, name: "n1", node: node, secrets: ta.secrets}
	for content, want := range map[string]string{"pw\n": "pw", "pw \n\n": "pw \n", "pw": "pw"} {
		path := filepath.Join(ta.dir, "password")
		writeFile(t, path, content)
		if got, err := ta.fallbackSecret(tg, path, true); err != nil || got != want {
			t.Errorf("password from %q = %q, %v, want %q", content, got, err, want)
		}
	}
}

// installedNode is a normal-mode chalkd whose STATE holds the test node's identity.
func installedNode(t *testing.T, ta *testApp, recorded []byte) (*chalkd.Server, *fakeRunner) {
	t.Helper()
	root := t.TempDir()
	r := &fakeRunner{}
	s := &chalkd.Server{
		Mode: nodev1.Mode_MODE_NORMAL,
		Paths: chalkd.Paths{
			StateDir:       filepath.Join(root, "state"),
			BootDisk:       "/dev/disk/chalk-boot-disk",
			BootPartitions: "/dev/disk/chalk-boot",
			StorageStatus:  filepath.Join(root, "storage-status.json"),
			MountInfo:      filepath.Join(root, "mountinfo"),
		},
		Run:        r,
		Host:       storage.Host{SysRoot: filepath.Join(root, "sys"), UdevRoot: filepath.Join(root, "udev"), DevRoot: filepath.Join(root, "dev")},
		RebootNode: func() {},
	}
	s.Identity.RunDir = filepath.Join(root, "run")
	s.Identity.NetworkDir = filepath.Join(root, "network")
	s.Identity.Consumers = filepath.Join(root, "consumers.json")
	s.Identity.SetHostname = func(string) error { return nil }
	writeFile(t, filepath.Join(s.Paths.StateDir, "identity.json"), string(recorded))
	section, _ := json.Marshal(ta.manifest.Nodes["n1"].Identity.Storage)
	writeFile(t, filepath.Join(s.Paths.StateDir, "storage", "storage.json"), string(section))
	writeFile(t, s.Paths.MountInfo, "41 30 253:1 / /var rw - ext4 /dev/mapper/var rw\n")
	return s, r
}

type fakeRunner struct{ calls []string }

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f.RunWithEnv(ctx, nil, name, args...)
}

func (f *fakeRunner) RunWithEnv(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	return nil, nil
}

func TestApplyIdentityAndStatus(t *testing.T) {
	ta := newTestApp(t)
	current, _ := json.Marshal(ta.manifest.Nodes["n1"].Identity)
	s, r := installedNode(t, ta, []byte(`{"hostname": "old"}`))
	addr := ta.startNode(t, s)

	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stdout.String(), "chalkctl apply-identity n1 delivers it") || !strings.Contains(ta.stdout.String(), "var") {
		t.Errorf("status before = %q", ta.stdout)
	}

	ta.stdout.Reset()
	r.calls = nil
	if err := ta.run(context.Background(), ta.args([]string{"apply-identity", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.calls, []string{"networkctl reload"}) {
		t.Errorf("node ran %v", r.calls)
	}
	if got, _ := os.ReadFile(filepath.Join(s.Paths.StateDir, "identity.json")); !bytes.Equal(got, current) {
		t.Errorf("identity on the node = %s", got)
	}

	ta.stdout.Reset()
	r.calls = []string{}
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stdout.String(), "(the cluster definition's)") {
		t.Errorf("status after = %q", ta.stdout)
	}
}

func TestApplyIdentityNamesResetCommand(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, []byte(`{}`))
	// The node recorded VAR as xfs, so the definition's ext4 is a destructive change.
	section := ta.manifest.Nodes["n1"].Identity.Storage
	v := section.Volumes["var"]
	v.Format = "xfs"
	section.Volumes = map[string]storage.Volume{"var": v}
	data, _ := json.Marshal(section)
	writeFile(t, filepath.Join(s.Paths.StateDir, "storage", "storage.json"), string(data))
	addr := ta.startNode(t, s)

	err := ta.run(context.Background(), ta.args([]string{"apply-identity", "n1"}, addr))
	if err == nil || !strings.Contains(err.Error(), "chalkctl storage reset n1 var") {
		t.Fatalf("err = %v, want the reset command for n1", err)
	}
}

func TestInstalledNodeRequiresOSCA(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	// Secrets of another cluster: the node's certificate is not from their CA.
	other := newTestApp(t)
	err := other.run(context.Background(), other.args([]string{"status", "n1"}, addr))
	if err == nil {
		t.Fatal("trusted a node whose certificate another CA issued")
	}
}

func TestLoadClusterEvaluatesFlake(t *testing.T) {
	ta := newTestApp(t)
	var calls []string
	ta.nix = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch {
		case strings.HasSuffix(args[2], "#chalkos"):
			return []byte(`["lab"]`), nil
		case strings.HasSuffix(args[2], "#chalkos.lab.manifest"):
			return []byte(testManifest), nil
		case args[0] == "build":
			return []byte("/nix/store/x-chalkos\n"), nil
		}
		return nil, errors.New("unexpected")
	}
	c, err := ta.loadCluster(context.Background(), clusterFlags{flake: "/src/lab"})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := ta.buildImage(context.Background(), c, "test")
	if err != nil || dir != "/nix/store/x-chalkos" {
		t.Fatalf("image = %q, %v", dir, err)
	}
	want := []string{
		"eval --json /src/lab#chalkos --apply builtins.attrNames",
		"eval --json /src/lab#chalkos.lab.manifest",
		"build --no-link --print-out-paths /src/lab#chalkos.lab.roles.test.image",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("nix calls = %v, want %v", calls, want)
	}
}

func TestEndpointNeedsAnAddress(t *testing.T) {
	if _, err := endpoint("", "n2", manifest.Identity{}); err == nil || !strings.Contains(err.Error(), "--endpoint") {
		t.Errorf("err = %v", err)
	}
	if got, _ := endpoint("", "n1", manifest.Identity{Network: map[string]any{"networks": map[string]any{
		"20-b": map[string]any{"address": []any{"10.0.1.5/24"}},
		"10-a": map[string]any{"address": []any{"10.0.0.11/24"}},
	}}}); got != "10.0.0.11" {
		t.Errorf("endpoint = %q, want the first network's address", got)
	}
}
