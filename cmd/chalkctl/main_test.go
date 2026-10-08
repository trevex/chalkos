package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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
	// prompts records what chalkctl asked on the terminal; answers are given in order.
	prompts []string
	answers []string
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
	ta.readSecret = func(_ context.Context, prompt string) ([]byte, error) {
		ta.prompts = append(ta.prompts, prompt)
		if len(ta.answers) == 0 {
			return nil, errNoTerminal
		}
		answer := ta.answers[0]
		ta.answers = ta.answers[1:]
		return []byte(answer), nil
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
		cert, err = pki.IssueNode(ta.secrets.NodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, time.Now())
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
	return serveTLS(t, s.Handler(), chalkd.TLSConfig(chalkd.StaticCertificate(&pair), pool))
}

// serveTLS serves h with the configuration as chalkd does, and returns the address. httptest's
// servers would add a certificate of their own, which TLS prefers to GetCertificate.
func serveTLS(t *testing.T, h http.Handler, cfg *tls.Config) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, TLSConfig: cfg}
	go srv.ServeTLS(ln, "", "")
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
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
	if err := json.Unmarshal(pubData, &pub); err != nil || pub.OSCA.Certificate == "" || pub.OSCA.Key != "" || pub.NodeCA.Certificate == "" || pub.NodeCA.Key != "" || bytes.Contains(pubData, []byte("PRIVATE")) {
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
	nodeCert, err := pki.VerifyNode(string(got.NodeCertificate), string(got.NodeKey), ta.secrets.OSCA.Certificate, time.Now())
	if err != nil {
		t.Fatalf("node certificate: %v", err)
	}
	cert := nodeCert.Leaf
	if err := cert.VerifyHostname("n1"); err != nil {
		t.Errorf("node certificate: %v", err)
	}
	if issuer, _ := pki.ParseCertificate([]byte(ta.secrets.NodeCA.Certificate)); cert.CheckSignatureFrom(issuer) != nil {
		t.Error("the node certificate is not the node CA's")
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
	for content, want := range map[string]string{
		"pw\n": "pw", "pw \n\n": "pw \n", "pw": "pw",
		"pw\r\n": "pw", "pw\r": "pw", "pw\r\r\n": "pw\r", "pw\n\r\n": "pw\n",
	} {
		path := filepath.Join(ta.dir, "password")
		writeFile(t, path, content)
		if got, err := ta.fallbackSecret(context.Background(), tg, path, true); err != nil || got != want {
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

func (f *fakeRunner) RunWithInput(ctx context.Context, _ []byte, name string, args ...string) ([]byte, error) {
	return f.RunWithEnv(ctx, nil, name, args...)
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
	if !reflect.DeepEqual(r.calls, []string{"systemctl list-units --state=failed --plain --no-legend --no-pager", "networkctl reload"}) {
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

// editManifest changes the test cluster's manifest and writes it where --manifest reads it.
func (ta *testApp) editManifest(t *testing.T, edit func(m *manifest.Manifest)) {
	t.Helper()
	edit(ta.manifest)
	data, err := json.Marshal(ta.manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ta.dir, "manifest.json"), string(data))
}

func TestInstalledNodeRequiresOSCA(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatalf("the node does not accept this cluster's admin: %v", err)
	}

	// This cluster's admin certificate, which the node accepts, but another cluster's OS CA:
	// chalkctl itself must refuse the node's certificate.
	other := ta.secrets
	other.OSCA = newTestApp(t).secrets.OSCA
	data, _ := other.Encode()
	otherSecrets := filepath.Join(ta.dir, "other", "secrets.json")
	writeFile(t, otherSecrets, string(data))
	err := ta.run(context.Background(), ta.args([]string{"status", "n1", "--secrets", otherSecrets}, addr))
	if err == nil || !strings.Contains(err.Error(), "x509") || !strings.Contains(err.Error(), "unknown authority") {
		t.Fatalf("err = %v, want the node's certificate refused as signed by an unknown authority", err)
	}

	// The node's certificate names n1, so it cannot pass for n2.
	ta.editManifest(t, func(m *manifest.Manifest) {
		n2 := m.Nodes["n1"]
		n2.Identity.Hostname = "n2"
		m.Nodes["n2"] = n2
	})
	err = ta.run(context.Background(), ta.args([]string{"status", "n2"}, addr))
	if err == nil || !strings.Contains(err.Error(), "x509") || !strings.Contains(err.Error(), "not n2") {
		t.Fatalf("err = %v, want the node's certificate refused for the name n2", err)
	}
}

func TestLoadClusterEvaluatesFlake(t *testing.T) {
	ta := newTestApp(t)
	// Dots in cluster and role names are part of the names, not attribute separators.
	ta.editManifest(t, func(m *manifest.Manifest) {
		m.Roles = map[string]manifest.Role{"web.v2": {Image: "roles.web.v2.image"}}
	})
	manifestJSON, _ := json.Marshal(ta.manifest)
	imageDir := t.TempDir()
	var calls []string
	ta.nix = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch {
		case strings.HasSuffix(args[2], "#chalkos"):
			return []byte(`["home.lab"]`), nil
		case strings.HasSuffix(args[2], `#chalkos."home.lab".manifest`):
			return manifestJSON, nil
		case args[0] == "build":
			return []byte(imageDir + "\n"), nil
		}
		return nil, errors.New("unexpected")
	}
	c, err := ta.loadCluster(context.Background(), clusterFlags{flake: "/src/lab"})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := ta.buildImage(context.Background(), c, "web.v2")
	if err != nil || dir != imageDir {
		t.Fatalf("image = %q, %v", dir, err)
	}
	want := []string{
		"eval --json /src/lab#chalkos --apply builtins.attrNames",
		`eval --json /src/lab#chalkos."home.lab".manifest`,
		`build --no-link --print-out-paths /src/lab#chalkos."home.lab".roles."web.v2".image^out`,
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("nix calls = %v, want %v", calls, want)
	}
}

func TestAttrName(t *testing.T) {
	for name, want := range map[string]string{
		"lab":      `"lab"`,
		"home.lab": `"home.lab"`,
		"a b#c?d":  `"a%20b%23c%3Fd"`,
		`50%\x`:    `"50%25%5Cx"`,
		"ä":        `"%C3%A4"`,
	} {
		if got, err := attrName(name); err != nil || got != want {
			t.Errorf("attrName(%q) = %s, %v, want %s", name, got, err, want)
		}
	}
	if _, err := attrName(`a"b`); err == nil {
		t.Error(`accepted a name with ", which a Nix attribute path cannot quote`)
	}
}

func TestBuildImageChecksOutputPath(t *testing.T) {
	ta := newTestApp(t)
	c := &cluster{flags: clusterFlags{flake: "/src/lab"}, attr: `chalkos."lab"`, manifest: ta.manifest}
	for name, out := range map[string]string{
		"several paths":  t.TempDir() + "\n" + t.TempDir() + "\n",
		"no path":        "\n",
		"a missing path": filepath.Join(ta.dir, "missing") + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			ta.nix = func(context.Context, ...string) ([]byte, error) { return []byte(out), nil }
			if dir, err := ta.buildImage(context.Background(), c, "test"); err == nil {
				t.Errorf("image = %q, want an error", dir)
			}
		})
	}
}

func TestPasswordPromptConfirms(t *testing.T) {
	ta := newTestApp(t)
	node := ta.manifest.Nodes["n1"]
	node.Identity.Storage.Fallback = "password"
	tg := &target{cluster: &cluster{manifest: ta.manifest}, name: "n1", node: node, secrets: ta.secrets}

	ta.answers = []string{"secret one", "secret one"}
	if got, err := ta.fallbackSecret(context.Background(), tg, "", true); err != nil || got != "secret one" {
		t.Errorf("password = %q, %v", got, err)
	}
	if len(ta.prompts) != 2 {
		t.Errorf("prompts = %q, want the password asked twice", ta.prompts)
	}

	ta.prompts, ta.answers = nil, []string{"secret one", "secret on"}
	if _, err := ta.fallbackSecret(context.Background(), tg, "", true); err == nil || !strings.Contains(err.Error(), "differ") {
		t.Errorf("err = %v, want the typo noticed", err)
	}

	ta.prompts, ta.answers = nil, nil
	if _, err := ta.fallbackSecret(context.Background(), tg, "", true); err == nil || !strings.Contains(err.Error(), "--password-file") {
		t.Errorf("err = %v, want --password-file suggested without a terminal", err)
	}
}

// withPassword gives n1 the password fallback and the volume data, encrypted as given.
func withPassword(t *testing.T, ta *testApp, encryption string) {
	ta.editManifest(t, func(m *manifest.Manifest) {
		n := m.Nodes["n1"]
		n.Identity.Storage.Fallback = "password"
		n.Identity.Storage.Disks[storage.SystemDisk].Repart["60-data.conf"] = "[Partition]\nLabel=data\n"
		n.Identity.Storage.Volumes["data"] = storage.Volume{Disk: storage.SystemDisk, Label: "data", Format: "ext4", MountPoint: "/srv/data", Encryption: encryption, Size: "1G"}
		m.Nodes["n1"] = n
	})
}

func TestApplyIdentityAsksForPasswordOnlyForNewEncryptedVolumes(t *testing.T) {
	ta := newTestApp(t)
	ta.editManifest(t, func(m *manifest.Manifest) {
		n := m.Nodes["n1"]
		n.Identity.Storage.Fallback = "password"
		m.Nodes["n1"] = n
	})
	s, r := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)

	// Nothing to enroll: the password is not asked for.
	if err := ta.run(context.Background(), ta.args([]string{"apply-identity", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if len(ta.prompts) != 0 {
		t.Errorf("asked %q for an identity that enrolls nothing", ta.prompts)
	}

	// The new encrypted volume gets the password: it is asked twice, and a typo stops the
	// command before the identity is sent.
	withPassword(t, ta, storage.EncryptionTPM2)
	r.calls = nil
	ta.answers = []string{"right", "wrong"}
	err := ta.run(context.Background(), ta.args([]string{"apply-identity", "n1"}, addr))
	if err == nil || !strings.Contains(err.Error(), "differ") {
		t.Fatalf("err = %v, want the typo noticed", err)
	}
	if len(ta.prompts) != 2 || !reflect.DeepEqual(r.calls, []string{"systemctl list-units --state=failed --plain --no-legend --no-pager"}) {
		t.Errorf("prompts = %q, node ran %v", ta.prompts, r.calls)
	}
}

func TestStorageResetAsksForPasswordOnlyForEncryptedVolume(t *testing.T) {
	for _, c := range []struct {
		encryption string
		prompts    int
	}{{storage.EncryptionNone, 0}, {storage.EncryptionTPM2, 2}} {
		t.Run(c.encryption, func(t *testing.T) {
			ta := newTestApp(t)
			withPassword(t, ta, c.encryption)
			s, _ := installedNode(t, ta, []byte(`{}`))
			addr := ta.startNode(t, s)
			ta.answers = []string{"right", "wrong"}
			// The node fails to find the volume's partition; only the prompts matter here.
			ta.run(context.Background(), ta.args([]string{"storage", "reset", "n1", "data"}, addr))
			if len(ta.prompts) != c.prompts {
				t.Errorf("prompts = %q, want %d", ta.prompts, c.prompts)
			}
		})
	}
}

func TestFallbackNeeded(t *testing.T) {
	section := storage.Section{Fallback: "password", Volumes: map[string]storage.Volume{
		"var":   {Encryption: storage.EncryptionTPM2},
		"data":  {Encryption: storage.EncryptionTPM2},
		"cache": {Encryption: storage.EncryptionNone},
	}}
	for _, c := range []struct {
		existing []string
		reset    string
		want     bool
	}{
		{[]string{"var", "data", "cache"}, "", false},
		{[]string{"var", "cache"}, "", true},
		{[]string{"var", "data"}, "", false},
		{[]string{"var", "data", "cache"}, "data", true},
		{[]string{"var", "data", "cache"}, "cache", false},
	} {
		if got := fallbackNeeded(section, c.existing, c.reset); got != c.want {
			t.Errorf("fallbackNeeded(existing %v, reset %q) = %v, want %v", c.existing, c.reset, got, c.want)
		}
	}
	section.Fallback = storage.FallbackNone
	if fallbackNeeded(section, nil, "data") {
		t.Error("needed a secret for a node without fallback")
	}
}

func TestReadInterruptible(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	defer close(release)
	restored := false
	done := make(chan error)
	go func() {
		_, err := readInterruptible(ctx, func() ([]byte, error) { <-release; return []byte("late"), nil }, func() { restored = true })
		done <- err
	}()
	cancel()
	if err := <-done; !errors.Is(err, errInterrupted) || !restored {
		t.Errorf("err = %v, restored = %v, want an interruption that restores the terminal", err, restored)
	}
	if exitStatus(fmt.Errorf("read: %w", errInterrupted)) != 130 {
		t.Error("an interrupted prompt does not exit with status 130")
	}

	got, err := readInterruptible(context.Background(), func() ([]byte, error) { return []byte("pw"), nil }, func() { t.Error("restored after a completed read") })
	if err != nil || string(got) != "pw" {
		t.Errorf("read = %q, %v", got, err)
	}
}

func TestExitStatus(t *testing.T) {
	for err, want := range map[error]int{nil: 0, errUsage: 2, errors.New("x"): 1, errInterrupted: 130} {
		if got := exitStatus(err); got != want {
			t.Errorf("exitStatus(%v) = %d, want %d", err, got, want)
		}
	}
}

func TestDefaultIdentitiesSkipUnreadable(t *testing.T) {
	ta := newTestApp(t)
	id, _ := age.GenerateX25519Identity()
	writeFile(t, filepath.Join(ta.dir, ".config", "chalkos", "age.key"), id.String()+"\n")
	// An encrypted old-format SSH key without its .pub cannot be used.
	broken := filepath.Join(ta.dir, ".ssh", "id_rsa")
	writeFile(t, broken, "-----BEGIN RSA PRIVATE KEY-----\nProc-Type: 4,ENCRYPTED\nsecret material\n-----END RSA PRIVATE KEY-----\n")
	ids, err := ta.ageIdentities(context.Background(), nil)
	if err != nil || len(ids) != 1 {
		t.Fatalf("identities = %v, %v, want the age key", ids, err)
	}
	warning := ta.stderr.String()
	if !strings.Contains(warning, broken) || strings.Contains(warning, "secret material") || strings.Count(warning, "\n") != 1 {
		t.Errorf("warning = %q, want one line naming the file", warning)
	}

	// No default identity loads.
	os.Remove(filepath.Join(ta.dir, ".config", "chalkos", "age.key"))
	if _, err := ta.ageIdentities(context.Background(), nil); err == nil {
		t.Error("no error without a usable identity")
	}
	// An identity named on the command line must load.
	if _, err := ta.ageIdentities(context.Background(), []string{broken}); err == nil || !strings.Contains(err.Error(), broken) {
		t.Errorf("err = %v, want the named identity's failure", err)
	}
}

func TestSignedCopyCleansUp(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	dir := t.TempDir()
	raw := filepath.Join(dir, "chalkos.raw")
	writeFile(t, raw, "image")
	for name, src := range map[string]string{
		"missing image":    filepath.Join(dir, "missing.raw"),
		"unreadable image": dir,
		// Signing fails: there is no repart-output.json next to the image.
		"signing fails": raw,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := signedCopy(context.Background(), src, "key.pem", "cert.pem"); err == nil {
				t.Fatal("no error")
			}
			left, _ := filepath.Glob(filepath.Join(cache, "chalkctl", "*"))
			if len(left) != 0 {
				t.Errorf("left %v behind", left)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(cache, "chalkctl")); err != nil {
		t.Errorf("the copy was not made in the cache directory: %v", err)
	}
}

func TestCreateNewRemovesPartialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	err := createNew(path, 0o600, func(w io.Writer) error {
		w.Write([]byte("half a sec"))
		return errors.New("disk full")
	})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the partial file is left: %v", err)
	}
	if err := writeNew(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(path, []byte("other"), 0o600); err == nil {
		t.Error("overwrote an existing file")
	}
	if got, _ := os.ReadFile(path); string(got) != "data" {
		t.Errorf("file = %q, want the existing file kept", got)
	}
}

func TestAdminCertificateLastsOneRun(t *testing.T) {
	ta := newTestApp(t)
	pair, err := adminCertificate(ta.secrets)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := pki.ParseCertificate([]byte(ta.secrets.OSCA.Certificate))
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	chains, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	if err != nil {
		t.Fatal(err)
	}
	if role, _ := pki.ClientRole(chains); role != pki.RoleAdmin || cert.Subject.CommonName != "chalkctl" {
		t.Errorf("certificate %s has role %q", cert.Subject, role)
	}
	if got := cert.NotAfter.Sub(time.Now()); got > time.Hour || got < 59*time.Minute {
		t.Errorf("the admin certificate lasts another %v, want an hour", got)
	}
}

func TestOlderSecretsFilesAreRefused(t *testing.T) {
	ta := newTestApp(t)
	for _, version := range []string{"1", "2"} {
		writeFile(t, filepath.Join(ta.dir, "secrets.json"), `{"version": `+version+`, "osCA": {}, "admin": {}}`)
		err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, "127.0.0.1:1"))
		if err == nil || !strings.Contains(err.Error(), "version "+version+" is not supported") || !strings.Contains(err.Error(), "chalkctl gen secrets") {
			t.Errorf("version %s: %v, want a refusal naming chalkctl gen secrets", version, err)
		}
	}
}
