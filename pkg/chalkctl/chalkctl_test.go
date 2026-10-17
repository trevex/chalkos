package chalkctl

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
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/config/store"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/uki"
	"github.com/trevex/chalkos/pkg/upgrade"
)

func TestRunSignRequiresAllFlags(t *testing.T) {
	err := runSign(context.Background(), signFlags{image: "disk.raw"})
	if err == nil || !strings.Contains(err.Error(), "--repart-json") {
		t.Fatalf("err = %v, want a missing --repart-json error", err)
	}
}

// testApp is chalkctl with the test cluster's inventory and a plaintext secrets file in a
// temporary directory.
type testApp struct {
	*app
	dir string
	// inventory is the test cluster's inventory in dir.
	inventory string
	secrets   pki.Secrets
	stdout    *bytes.Buffer
	stderr    *bytes.Buffer
	// kind is n1's Kubernetes kind, which withKind gives it.
	kind string
	// prompts records what chalkctl asked on the terminal; answers are given in order.
	prompts []string
	answers []string
}

func newTestApp(t *testing.T) *testApp {
	t.Helper()
	return newTestAppAt(t, time.Now())
}

// newTestAppAt is newTestApp with secrets generated at now.
func newTestAppAt(t *testing.T, now time.Time) *testApp {
	t.Helper()
	dir := t.TempDir()
	secrets, err := pki.GenerateSecrets(now)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := secrets.Encode()
	writeFile(t, filepath.Join(dir, "secrets.json"), string(data))
	ta := &testApp{dir: dir, inventory: writeInventory(t, dir), secrets: secrets, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	ta.app = &app{
		stdin:  strings.NewReader(""),
		stdout: ta.stdout,
		stderr: ta.stderr,
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

// args appends the flags that point chalkctl at the test cluster and the node's address.
func (ta *testApp) args(cmd []string, addr string) []string {
	return append(cmd, "--inventory", ta.inventory, "--address", addr)
}

// writeNode replaces n1's file with the documents given.
func (ta *testApp) writeNode(t *testing.T, docs ...string) {
	t.Helper()
	writeFile(t, filepath.Join(ta.dir, "nodes", "n1.yaml"), strings.Join(docs, "---\n"))
}

// configVersion is the version of n1's config as the inventory gives it.
func (ta *testApp) configVersion(t *testing.T) string {
	t.Helper()
	return inventoryVersion(t, ta.inventory)
}

// identity is n1's identity as its config translates on the image its Image names.
func (ta *testApp) identity(t *testing.T) identity.Identity {
	t.Helper()
	c, err := loadInventory(ta.inventory)
	if err != nil {
		t.Fatal(err)
	}
	nc, err := c.config("n1")
	if err != nil {
		t.Fatal(err)
	}
	md, err := nc.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	ct, err := imageContract(nc.ImageDir)
	if err != nil {
		t.Fatal(err)
	}
	ct.platform = md.Platform
	d, err := checkNodeConfig(nc, ct)
	if err != nil {
		t.Fatal(err)
	}
	return d.identity
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
	// The node runs the image the test node's Image names.
	if s.Paths.OSRelease == "" {
		s.Paths.OSRelease = filepath.Join(t.TempDir(), "os-release")
		writeFile(t, s.Paths.OSRelease, "IMAGE_ID=chalkos\nIMAGE_VERSION=\"\"\nCHALKOS_VARIANT=test\nCHALKOS_PLATFORM=metal\n")
	}
	if s.Loader.Schemas == "" {
		s.Loader.Schemas = filepath.Join(t.TempDir(), "schemas")
		copySchemas(t, s.Loader.Schemas)
	}
	// It boots the store of the root hash abab…, whose config slot an install in place writes.
	if s.Paths.Cmdline == "" {
		s.Paths.Cmdline = filepath.Join(t.TempDir(), "cmdline")
		writeFile(t, s.Paths.Cmdline, "init=/x usrhash="+strings.Repeat("ab", 32)+"\n")
	}
	ca, _ := pki.ParseCertificate([]byte(ta.secrets.OSCA.Certificate))
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	s.ClientCAs = chalkd.StaticCAs(pool)
	return serveTLS(t, s.Handler(), chalkd.TLSConfig(chalkd.StaticCertificate(&pair), s.ClientCAs))
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
		FromParts:  func(context.Context, install.PartsRequest) error { return errors.New("unexpected install onto a disk") },
		RebootNode: func() {},
	}
}

// fixtureSchemas are the schemas of the built-in kinds and of networkd's Network.
var fixtureSchemas = []string{"../../test/fixtures/kinds/builtin", "../../test/fixtures/config/networkd/schemas"}

// copySchemas writes the fixture schemas into dir, as an image ships them.
func copySchemas(t *testing.T, dir string) {
	t.Helper()
	for _, src := range fixtureSchemas {
		entries, err := os.ReadDir(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			data, err := os.ReadFile(filepath.Join(src, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(dir, e.Name()), string(data))
		}
	}
}

// clusterFile is the cluster file chalkd writes for a node of the kind.
func clusterFile(kind string) string {
	return `{"kind": "` + kind + `", "endpoint": "https://10.0.0.10:6443", "version": "1.37.1",
	  "ipFamilies": ["ipv4"], "podCIDRs": {"ipv4": "10.244.0.0/16"}, "serviceCIDRs": {"ipv4": "10.96.0.0/12"},
	  "dnsIPs": {"ipv4": "10.96.0.10"}, "nodeCIDRMaskSizes": {"ipv4": 24}, "domain": "cluster.local",
	  "nodeIP": {"validSubnets": [], "timeout": 300}, "vxlanSourceSubnets": [], "vip": {"addresses": [], "mode": "l2"},
	  "extraArgs": {}, "images": {"etcd": "e", "kubeAPIServer": "a", "kubeControllerManager": "c", "kubeScheduler": "s"}}`
}

// testImageJSON is the image.json of a Kubernetes image without flannel.
const testImageJSON = `{"version": "1.37.1", "cni": "none", "images": {"etcd": "e", "kubeAPIServer": "a",
  "kubeControllerManager": "c", "kubeScheduler": "s", "kubeProxy": "p", "coredns": "d", "flannel": ""}}`

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

	// The encrypted file, decrypted with the default identity.
	inventory := writeInventory(t, out)
	ta.stdout.Reset()
	if err := ta.run(context.Background(), []string{"recovery-key", "n1", "--secrets", filepath.Join(out, "secrets.age"), "--inventory", inventory}); err != nil {
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
	if err := ta.run(context.Background(), []string{"recovery-key", "n1", "--secrets", "-", "--inventory", inventory}); err != nil {
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
	want := ta.configVersion(t)
	if got.Config.Version != want || got.Section.Disks[storage.SystemDisk].Ref.Selector.Serial != "chalk-target" {
		t.Errorf("install request config = %+v, section = %+v; want the config of version %s", got.Config, got.Section, want)
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

// TestInstallInPlaceRefusesAnotherPlatform refuses to install a node in place that runs an image
// of another platform than its Image's.
func TestInstallInPlaceRefusesAnotherPlatform(t *testing.T) {
	ta := newTestApp(t)
	writeImageDir(t, filepath.Join(ta.dir, "images", "test-metal"), "test", "kvm", testRootHash, false)
	s := maintenanceNode()
	called := false
	s.InPlace = func(context.Context, install.Request) error { called = true; return nil }
	addr := ta.startNode(t, s)
	err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--fingerprint", s.Fingerprint}, addr))
	if want := "n1 runs an image built for metal, but its Image test-metal is built for kvm; changing a node's platform is a reinstall"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("install = %v, want %q", err, want)
	}
	if called {
		t.Error("installed a node of another platform")
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

// TestInstallFromPartsStreamsTheImage sends the installer the image's store, hash tree, UKI and
// boot loader with the header describing them, and the image's definitions. The image may be
// another than the node's Image names: the installer checks the config against the image's schemas.
func TestInstallFromPartsStreamsTheImage(t *testing.T) {
	ta := newTestApp(t)
	imageDir := testUpgradeImage(t, "gpu", "0.1.0")
	copySchemas(t, filepath.Join(imageDir, "schemas"))
	writeFile(t, filepath.Join(imageDir, "repart.d", "50-state.conf"), "[Partition]\nLabel=state\n")
	// The build named its revision and tag, which the node records.
	md, err := os.ReadFile(filepath.Join(imageDir, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(imageDir, "metadata.json"), strings.Replace(string(md), `"kinds"`, `"revision": "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", "tag": "v1.5.0", "kinds"`, 1))
	s := maintenanceNode()
	s.Installer = true
	var got install.PartsRequest
	var streamed []byte
	s.FromParts = func(_ context.Context, req install.PartsRequest) error {
		got = req
		var err error
		streamed, err = io.ReadAll(req.Parts)
		return err
	}
	addr := ta.startNode(t, s)
	fp := s.Fingerprint

	if err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--fingerprint", fp, "--image", imageDir}, addr)); err != nil {
		t.Fatal(err)
	}
	h := got.Image
	if h.ImageID != "chalkos" || len(h.RootHash) != 32 || h.Platform != "metal" || h.BootLoaderSize == 0 ||
		h.Provenance != (upgrade.Provenance{Revision: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", Tag: "v1.5.0"}) {
		t.Errorf("image = %+v", h)
	}
	if want := "chalkos " + imageName(h.RootHash, h.Provenance.Revision, h.Provenance.Tag) + ", sending"; !strings.Contains(ta.stdout.String(), want) || !strings.Contains(want, "(v1.5.0, a1b2c3d4e5f6)") {
		t.Errorf("output lacks %q:\n%s", want, ta.stdout)
	}
	size := h.StoreSize + h.VeritySize + h.UKISize + h.BootLoaderSize
	loader := sha256.Sum256(streamed[max(len(streamed)-int(h.BootLoaderSize), 0):])
	if int64(len(streamed)) != size || !bytes.Equal(loader[:], h.BootLoaderSHA256) {
		t.Errorf("streamed %d bytes, want %d ending in the boot loader", len(streamed), size)
	}
	if !strings.Contains(ta.stdout.String(), fmt.Sprintf("sending %d bytes", size)) {
		t.Errorf("chalkctl printed %q, not the bytes it sends", ta.stdout)
	}
	if got.Target != (storage.Ref{Selector: storage.Selector{Serial: "chalk-target"}}) || got.WipeDisk {
		t.Errorf("target = %+v, wipe = %v", got.Target, got.WipeDisk)
	}
	if !reflect.DeepEqual(got.SystemDefinitions, map[string]string{"50-state.conf": "[Partition]\nLabel=state\n"}) {
		t.Errorf("definitions = %v", got.SystemDefinitions)
	}
}

// TestInstallChecksTheImage refuses images of another platform, and images whose UKI or
// boot loader the cluster's db certificate did not sign, before the installer is sent anything;
// --sign-key signs both.
func TestInstallChecksTheImage(t *testing.T) {
	if _, err := exec.LookPath("sbsign"); err != nil {
		t.Skip("sbsign not in PATH")
	}
	ta := newTestApp(t)
	keys := t.TempDir()
	dbKey, dbCert := testSigner(t, keys, "db")
	otherKey, otherCert := testSigner(t, keys, "other")
	certPEM, _ := os.ReadFile(dbCert)
	image := func(variant string) string {
		dir := testUpgradeImage(t, variant, "0.1.0")
		writeFile(t, filepath.Join(dir, "repart.d", "50-state.conf"), "[Partition]\nLabel=state\n")
		copySchemas(t, filepath.Join(dir, "schemas"))
		return dir
	}
	s := maintenanceNode()
	s.Installer = true
	var parts []byte
	var header upgrade.Header
	s.FromParts = func(_ context.Context, req install.PartsRequest) error {
		var err error
		header = req.Image
		parts, err = io.ReadAll(req.Parts)
		return err
	}
	addr := ta.startNode(t, s)
	img := image("test")
	signedByOther := image("test")
	signTestImage(t, signedByOther, otherKey, otherCert)
	for _, tc := range []struct {
		name string
		img  string
		args []string
		want string
	}{
		{"another platform", func() string {
			dir := testUpgradeImageOfBlocks(t, "test", "kvm", "0.1.0", 512)
			writeFile(t, filepath.Join(dir, "repart.d", "50-state.conf"), "[Partition]\nLabel=state\n")
			copySchemas(t, filepath.Join(dir, "schemas"))
			return dir
		}(), []string{"--sign-key", dbKey, "--sign-cert", dbCert}, "n1 runs on metal; the image is built for kvm"},
		{"an unsigned image", img, []string{"--sign-cert", dbCert}, "Secure Boot would refuse the image's UKI"},
		{"an image signed by another key", signedByOther, []string{"--sign-cert", dbCert}, "not in db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parts = nil
			args := append([]string{"install", "n1", "--fingerprint", s.Fingerprint, "--image", tc.img}, tc.args...)
			err := ta.run(context.Background(), ta.args(args, addr))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("install = %v, want %q", err, tc.want)
			}
			if parts != nil {
				t.Error("the installer was sent the image")
			}
		})
	}
	if err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--fingerprint", s.Fingerprint, "--image", img, "--sign-key", dbKey, "--sign-cert", dbCert}, addr)); err != nil {
		t.Fatal(err)
	}
	certs, err := pki.ParseBundle(string(certPEM))
	if err != nil {
		t.Fatal(err)
	}
	loader := parts[len(parts)-int(header.BootLoaderSize):]
	if err := uki.VerifySignature(bytes.NewReader(loader), header.BootLoaderSize, uki.Database{Certificates: certs}, uki.Database{}); err != nil {
		t.Errorf("the boot loader sent: %v", err)
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

func TestPasswordFileTrimsOneNewline(t *testing.T) {
	ta := newTestApp(t)
	tg := &target{cluster: &cluster{name: "lab"}, name: "n1", secrets: ta.secrets}
	for content, want := range map[string]string{
		"pw\n": "pw", "pw \n\n": "pw \n", "pw": "pw",
		"pw\r\n": "pw", "pw\r": "pw", "pw\r\r\n": "pw\r", "pw\n\r\n": "pw\n",
	} {
		path := filepath.Join(ta.dir, "password")
		writeFile(t, path, content)
		if got, err := ta.fallbackSecret(context.Background(), tg, "password", path, true); err != nil || got != want {
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
	s.Loader.RunDir = filepath.Join(root, "run")
	s.Loader.NetworkDir = filepath.Join(root, "network")
	s.Loader.Consumers = filepath.Join(root, "consumers.json")
	s.Loader.SetHostname = func(string) error { return nil }
	// The node runs the image of the root hash abab…, whose slot holds the identity alone.
	s.Loader.Slots = filepath.Join(root, "state", "config")
	s.Loader.Cmdline = filepath.Join(root, "cmdline")
	s.Paths.Cmdline = s.Loader.Cmdline
	writeFile(t, s.Loader.Cmdline, "init=/x usrhash="+strings.Repeat("ab", 32)+"\n")
	if err := store.Write(slotOf(t, s), store.Stored{Identity: recorded}); err != nil {
		t.Fatal(err)
	}
	section, _ := json.Marshal(ta.identity(t).Storage)
	writeFile(t, filepath.Join(s.Paths.StateDir, "storage", "storage.json"), string(section))
	// The storage rules compare the config with the section STATE records.
	s.Paths.Facts = filepath.Join(root, "facts.json")
	writeFile(t, s.Paths.Facts, `{"recorded": {"file": "`+filepath.Join(s.Paths.StateDir, "storage", "storage.json")+`"}}`)
	writeFile(t, s.Paths.MountInfo, "41 30 253:1 / /var rw - ext4 /dev/mapper/var rw\n")
	return s, r
}

// slotOf is the running image's config slot of a node installedNode made.
func slotOf(t *testing.T, s *chalkd.Server) string {
	t.Helper()
	dir, err := s.Loader.Slot()
	if err != nil {
		t.Fatal(err)
	}
	return dir
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

func TestApplyConfigAndStatus(t *testing.T) {
	ta := newTestApp(t)
	s, r := installedNode(t, ta, []byte(`{"hostname": "old"}`))
	addr := ta.startNode(t, s)

	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stdout.String(), "config none (not the inventory's; chalkctl apply-config n1 delivers it)") || !strings.Contains(ta.stdout.String(), "var") {
		t.Errorf("status before = %q", ta.stdout)
	}

	ta.stdout.Reset()
	r.calls = nil
	if err := ta.run(context.Background(), ta.args([]string{"apply-config", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.calls, []string{"systemctl list-units --state=failed --plain --no-legend --no-pager", "chronyc -n -c tracking", "networkctl reload", "chronyc reload sources"}) {
		t.Errorf("node ran %v", r.calls)
	}
	want := ta.configVersion(t)
	if v, err := store.Version(slotOf(t, s)); err != nil || v != want {
		t.Errorf("config version on the node = %q, %v; want %s", v, err, want)
	}
	if !strings.Contains(ta.stdout.String(), "n1 runs config "+want) {
		t.Errorf("stdout = %q", ta.stdout)
	}

	ta.stdout.Reset()
	r.calls = []string{}
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stdout.String(), "platform metal (the inventory's)") || !strings.Contains(ta.stdout.String(), "config "+want+" (the inventory's)") {
		t.Errorf("status after = %q", ta.stdout)
	}
}

func TestApplyConfigNamesTheRemedy(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, []byte(`{}`))
	// The node recorded VAR as xfs, so the inventory's ext4 is a destructive change, which only a
	// reinstall carries out, since a reset cannot recreate VAR while the node runs.
	section := ta.identity(t).Storage
	v := section.Volumes["var"]
	v.Format = "xfs"
	section.Volumes = map[string]storage.Volume{"var": v}
	data, _ := json.Marshal(section)
	writeFile(t, filepath.Join(s.Paths.StateDir, "storage", "storage.json"), string(data))
	addr := ta.startNode(t, s)

	err := ta.run(context.Background(), ta.args([]string{"apply-config", "n1"}, addr))
	if err == nil || !strings.Contains(err.Error(), "only a reinstall of n1 carries it out") {
		t.Fatalf("err = %v, want a reinstall of n1", err)
	}
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
	writeFile(t, ta.inventory, testInventoryYAML+"    n2: {address: 10.0.0.11, files: [nodes/n2.yaml]}\n")
	writeFile(t, filepath.Join(ta.dir, "nodes", "n2.yaml"), strings.Join([]string{strings.Replace(n1Node, "{name: n1}", "{name: n2}", 1), n1SystemDisk, n1Network}, "---\n"))
	err = ta.run(context.Background(), ta.args([]string{"status", "n2"}, addr))
	if err == nil || !strings.Contains(err.Error(), "x509") || !strings.Contains(err.Error(), "not n2") {
		t.Fatalf("err = %v, want the node's certificate refused for the name n2", err)
	}
}

func TestPasswordPromptConfirms(t *testing.T) {
	ta := newTestApp(t)
	tg := &target{cluster: &cluster{name: "lab"}, name: "n1", secrets: ta.secrets}

	ta.answers = []string{"secret one", "secret one"}
	if got, err := ta.fallbackSecret(context.Background(), tg, "password", "", true); err != nil || got != "secret one" {
		t.Errorf("password = %q, %v", got, err)
	}
	if len(ta.prompts) != 2 {
		t.Errorf("prompts = %q, want the password asked twice", ta.prompts)
	}

	ta.prompts, ta.answers = nil, []string{"secret one", "secret on"}
	if _, err := ta.fallbackSecret(context.Background(), tg, "password", "", true); err == nil || !strings.Contains(err.Error(), "differ") {
		t.Errorf("err = %v, want the typo noticed", err)
	}

	ta.prompts, ta.answers = nil, nil
	if _, err := ta.fallbackSecret(context.Background(), tg, "password", "", true); err == nil || !strings.Contains(err.Error(), "--password-file") {
		t.Errorf("err = %v, want --password-file suggested without a terminal", err)
	}
}

// withPassword gives n1 the password fallback and the volume data, encrypted as given.
func withPassword(t *testing.T, ta *testApp, encryption string) {
	ta.writeNode(t, n1Node, passwordSystemDisk, n1Network,
		"apiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: data}\nspec: {size: 1G, format: ext4, mountPoint: /srv/data, encryption: {mode: "+encryption+"}}\n")
}

func TestApplyConfigAsksForPasswordOnlyForNewEncryptedVolumes(t *testing.T) {
	ta := newTestApp(t)
	ta.writeNode(t, n1Node, passwordSystemDisk, n1Network)
	s, r := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)

	// Nothing to enroll: the password is not asked for.
	if err := ta.run(context.Background(), ta.args([]string{"apply-config", "n1"}, addr)); err != nil {
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
	err := ta.run(context.Background(), ta.args([]string{"apply-config", "n1"}, addr))
	if err == nil || !strings.Contains(err.Error(), "differ") {
		t.Fatalf("err = %v, want the typo noticed", err)
	}
	if len(ta.prompts) != 2 || !reflect.DeepEqual(r.calls, []string{"systemctl list-units --state=failed --plain --no-legend --no-pager", "chronyc -n -c tracking"}) {
		t.Errorf("prompts = %q, node ran %v", ta.prompts, r.calls)
	}
}

func TestStorageResetAsksForPasswordOnlyForEncryptedVolume(t *testing.T) {
	for _, c := range []struct {
		encryption string
		prompts    int
		// want is why the reset fails: the node has no partition of volume data yet, or the password
		// typed twice differs.
		want string
	}{{storage.EncryptionNone, 0, "volume data has no recorded partition"}, {storage.EncryptionTPM2, 2, "differ"}} {
		t.Run(c.encryption, func(t *testing.T) {
			ta := newTestApp(t)
			withPassword(t, ta, c.encryption)
			s, _ := installedNode(t, ta, []byte(`{}`))
			addr := ta.startNode(t, s)
			ta.answers = []string{"right", "wrong"}
			err := ta.run(context.Background(), ta.args([]string{"storage", "reset", "n1", "data"}, addr))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("reset = %v, want %q", err, c.want)
			}
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

// run runs chalkctl's command tree with args, with the test's streams, and returns the error of
// the command that ran.
func (a *app) run(ctx context.Context, args []string) error {
	root := newCommand(a)
	root.SetArgs(args)
	root.SetIn(a.stdin)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	return root.ExecuteContext(ctx)
}

// run runs chalkctl's command tree as a.run does, naming the test cluster's secrets file with
// --secrets, as an operator does, when the command takes one, args name neither a secrets file nor
// a client file, and the file exists.
func (ta *testApp) run(ctx context.Context, args []string) error {
	return ta.app.run(ctx, ta.withSecrets(args))
}

// withSecrets is args with the test cluster's secrets file named, where run names it.
func (ta *testApp) withSecrets(args []string) []string {
	path := filepath.Join(ta.dir, "secrets.json")
	if _, err := os.Stat(path); err != nil {
		return args
	}
	cmd, _, err := newCommand(ta.app).Find(args)
	if err != nil || cmd.Flags().Lookup("secrets") == nil {
		return args
	}
	for _, arg := range args {
		if arg == "--" {
			break
		}
		for _, flag := range []string{"--secrets", "--config"} {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return args
			}
		}
	}
	return append(slices.Clone(args), "--secrets", path)
}

// execute runs args as the chalkctl program does and returns its exit status.
func (a *app) execute(ctx context.Context, args ...string) int {
	root := newCommand(a)
	root.SetArgs(args)
	root.SetIn(a.stdin)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	return Execute(ctx, root)
}

func TestExecuteUsageErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"bogus"}, {"etcd"}, {"etcd", "bogus"}, {"gen"}} {
		ta := newTestApp(t)
		if got := ta.execute(context.Background(), args...); got != 2 {
			t.Errorf("chalkctl %v exits with %d, want 2", args, got)
		}
		if !strings.Contains(ta.stderr.String(), "Usage:") {
			t.Errorf("chalkctl %v prints no usage:\n%s", args, ta.stderr.String())
		}
		if len(args) > 0 && args[len(args)-1] == "bogus" && !strings.Contains(ta.stderr.String(), `unknown command "bogus"`) {
			t.Errorf("chalkctl %v does not name the unknown command:\n%s", args, ta.stderr.String())
		}
	}
}

// A flag error on the root or on a command with subcommands is a usage error, as an unknown
// command is. A flag before a subcommand's name is one, in either form.
func TestExecuteFlagUsageErrors(t *testing.T) {
	for _, c := range []struct {
		args []string
		flag string
	}{
		{[]string{"--bogus"}, "--bogus"},
		{[]string{"etcd", "--bogus"}, "--bogus"},
		{[]string{"etcd", "--force", "leave", "n1"}, "--force"},
		{[]string{"etcd", "--force=true", "leave", "n1"}, "--force"},
		{[]string{"etcd", "--via=cp2", "members"}, "--via"},
		{[]string{"etcd", "--via", "cp2", "members"}, "--via"},
		{[]string{"--insecure", "install", "cp1"}, "--insecure"},
		{[]string{"--insecure=true", "install", "cp1"}, "--insecure"},
	} {
		args, flag := c.args, c.flag
		ta := newTestApp(t)
		if got := ta.execute(context.Background(), args...); got != 2 {
			t.Errorf("chalkctl %v exits with %d, want 2", args, got)
		}
		if stderr := ta.stderr.String(); !strings.Contains(stderr, "unknown flag: "+flag) || !strings.Contains(stderr, "Usage:") {
			t.Errorf("chalkctl %v does not name %s and print the usage:\n%s", args, flag, stderr)
		}
	}
}

func TestExecuteErrors(t *testing.T) {
	ta := newTestApp(t)
	if got := ta.execute(context.Background(), "status", "n1", "--inventory", filepath.Join(ta.dir, "missing.yaml")); got != 1 {
		t.Errorf("a failing command exits with %d, want 1", got)
	}
	if !strings.HasPrefix(ta.stderr.String(), "chalkctl: ") {
		t.Errorf("stderr = %q, want the error after chalkctl:", ta.stderr.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ta = newTestApp(t)
	if got := ta.execute(ctx, "status", "n1", "--inventory", filepath.Join(ta.dir, "missing.yaml")); got != 130 {
		t.Errorf("a command whose context ended exits with %d, want 130", got)
	}
}

// Shell completion comes from cobra's completion command.
func TestCompletion(t *testing.T) {
	ta := newTestApp(t)
	if err := ta.run(context.Background(), []string{"completion", "bash"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stdout.String(), "__start_chalkctl") {
		t.Errorf("chalkctl completion bash prints no completion script:\n%.200s", ta.stdout.String())
	}
}

func TestExecuteFlags(t *testing.T) {
	ta := newTestApp(t)
	if got := ta.execute(context.Background(), "install", "n1", "--bogus"); got != 1 {
		t.Errorf("an unknown flag exits with %d, want 1", got)
	}
	if want := "chalkctl: unknown flag: --bogus; chalkctl install --help lists its flags\n"; ta.stderr.String() != want {
		t.Errorf("stderr = %q, want %q", ta.stderr.String(), want)
	}
	// Single-dash long flags are shorthands to pflag.
	ta = newTestApp(t)
	if got := ta.execute(context.Background(), "install", "n1", "-insecure"); got != 1 {
		t.Errorf("-insecure exits with %d, want 1", got)
	}
	ta = newTestApp(t)
	if got := ta.execute(context.Background(), "logs", "--help"); got != 0 {
		t.Errorf("--help exits with %d, want 0", got)
	}
	if !strings.Contains(ta.stdout.String(), "-f, --follow") {
		t.Errorf("logs --help lacks -f, --follow:\n%s", ta.stdout.String())
	}
}

// The help names the value of --address NODE=ADDR.
func TestAddressValueName(t *testing.T) {
	for _, name := range []string{"rotate", "upgrade"} {
		ta := newTestApp(t)
		if got := ta.execute(context.Background(), name, "--help"); got != 0 {
			t.Fatalf("%s --help exits with %d", name, got)
		}
		if !strings.Contains(ta.stdout.String(), "--address NODE=ADDR ") {
			t.Errorf("%s --help does not show --address NODE=ADDR:\n%s", name, ta.stdout.String())
		}
	}
}

// Every command is described for its help and the reference pages.
func TestCommandsDescribed(t *testing.T) {
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Short == "" || cmd.Long == "" || cmd.Example == "" {
			t.Errorf("%s lacks a short or long description or examples", cmd.CommandPath())
		}
		for _, c := range cmd.Commands() {
			walk(c)
		}
	}
	walk(newCommand(&app{}))
}

func TestCount(t *testing.T) {
	for _, tc := range []struct {
		got, want string
	}{
		{count(0, "node"), "0 nodes"},
		{count(1, "node"), "1 node"},
		{count(uint32(1), "object"), "1 object"},
		{count(uint64(19), "object"), "19 objects"},
		{count(1, "OS CA"), "1 OS CA"},
		{count(2, "OS CA"), "2 OS CAs"},
	} {
		if tc.got != tc.want {
			t.Errorf("%q, want %q", tc.got, tc.want)
		}
	}
}

// TestApplyConfigRefusesAnotherPlatform refuses to deliver config to a node whose image was built
// for another platform than its Image's: the node would translate the config
// for its own.
func TestApplyConfigRefusesAnotherPlatform(t *testing.T) {
	ta := newTestApp(t)
	s, r := installedNode(t, ta, []byte(`{}`))
	s.Paths.OSRelease = filepath.Join(t.TempDir(), "os-release")
	writeFile(t, s.Paths.OSRelease, "IMAGE_ID=chalkos\nIMAGE_VERSION=\"\"\nCHALKOS_VARIANT=test\nCHALKOS_PLATFORM=kvm\n")
	addr := ta.startNode(t, s)
	want := "n1 runs an image built for kvm, but its Image test-metal is built for metal; changing a node's platform is a reinstall"
	for _, cmd := range [][]string{{"apply-config", "n1"}, {"storage", "reset", "n1", "var"}} {
		err := ta.run(context.Background(), ta.args(cmd, addr))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s = %v, want %q", cmd[0], err, want)
		}
	}
	if len(r.calls) != 0 {
		t.Errorf("node ran %v for refused config", r.calls)
	}
}

// TestApplyConfigToAnImageOfAnotherVariant delivers config to a node that runs an image of another
// variant than its Image's: the node checks the config against its image's schemas.
func TestApplyConfigToAnImageOfAnotherVariant(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, []byte(`{"hostname": "old"}`))
	s.Paths.OSRelease = filepath.Join(t.TempDir(), "os-release")
	writeFile(t, s.Paths.OSRelease, "IMAGE_ID=chalkos\nIMAGE_VERSION=\"\"\nCHALKOS_VARIANT=w\nCHALKOS_PLATFORM=metal\n")
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"apply-config", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	want := ta.configVersion(t)
	if v, err := store.Version(slotOf(t, s)); err != nil || v != want {
		t.Errorf("config version on the node = %q, %v; want %s", v, err, want)
	}
}

// TestInstallInPlaceOnAnImageOfAnotherVariant installs a node in place that runs an image of
// another variant than its Image's: the node checks the config against its image's schemas.
func TestInstallInPlaceOnAnImageOfAnotherVariant(t *testing.T) {
	ta := newTestApp(t)
	s := maintenanceNode()
	called := false
	s.InPlace = func(context.Context, install.Request) error { called = true; return nil }
	s.Paths.OSRelease = filepath.Join(t.TempDir(), "os-release")
	writeFile(t, s.Paths.OSRelease, "IMAGE_ID=chalkos\nIMAGE_VERSION=\"\"\nCHALKOS_VARIANT=w\nCHALKOS_PLATFORM=metal\n")
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--fingerprint", s.Fingerprint}, addr)); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("the node was not installed in place")
	}
}

// TestApplyConfigStopsOnAFailingCheck sends nothing when the documents do not check against the
// schemas of the node's image: here the image lacks networkd's Network kind.
func TestApplyConfigStopsOnAFailingCheck(t *testing.T) {
	ta := newTestApp(t)
	s, r := installedNode(t, ta, []byte(`{"hostname": "old"}`))
	s.Loader.Schemas = filepath.Join(t.TempDir(), "schemas")
	entries, err := os.ReadDir(fixtureSchemas[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(fixtureSchemas[0], e.Name()))
		writeFile(t, filepath.Join(s.Loader.Schemas, e.Name()), string(data))
	}
	addr := ta.startNode(t, s)
	err = ta.run(context.Background(), ta.args([]string{"apply-config", "n1"}, addr))
	if err == nil || !strings.Contains(err.Error(), "the config of n1") {
		t.Fatalf("apply-config = %v, want the check's refusal", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("node ran %v", r.calls)
	}
	if got, _, _ := store.Read(slotOf(t, s)); string(got.Identity) != `{"hostname": "old"}` {
		t.Errorf("identity on the node = %s", got)
	}
	if v, err := store.Version(slotOf(t, s)); err != nil || v != "" {
		t.Errorf("config version on the node = %q, %v; want none", v, err)
	}
}

func TestImageContractNeedsSchemas(t *testing.T) {
	dir := t.TempDir()
	if _, err := imageContract(dir); err == nil || !strings.Contains(err.Error(), dir+" has no schemas/") {
		t.Errorf("err = %v", err)
	}
}

// TestInstallFromPartsSendsTheImageContract sends the installer the schemas and image.json
// of the image it installs, which it translates the config for.
func TestInstallFromPartsSendsTheImageContract(t *testing.T) {
	ta := newTestApp(t)
	withKind(t, ta, identity.KindWorker)
	imageDir := testUpgradeImage(t, "gpu", "0.1.0")
	copySchemas(t, filepath.Join(imageDir, "schemas"))
	writeFile(t, filepath.Join(imageDir, "image.json"), testImageJSON)
	writeFile(t, filepath.Join(imageDir, "repart.d", "50-state.conf"), "[Partition]\nLabel=state\n")
	s := maintenanceNode()
	s.Installer = true
	// The installer's own image has no schemas: the config checks against those sent alone.
	s.Loader.Schemas = filepath.Join(t.TempDir(), "none")
	var got install.PartsRequest
	s.FromParts = func(_ context.Context, req install.PartsRequest) error {
		got = req
		_, err := io.ReadAll(req.Parts)
		return err
	}
	addr := ta.startNode(t, s)
	if err := ta.run(context.Background(), ta.args([]string{"install", "n1", "--fingerprint", s.Fingerprint, "--image", imageDir}, addr)); err != nil {
		t.Fatal(err)
	}
	if got.Config.Version == "" || got.Kubernetes == nil || got.Kubernetes.NodeName != "n1" {
		t.Errorf("install request config = %+v, kubernetes = %+v; want both translated for the image sent", got.Config, got.Kubernetes)
	}
}

// TestStatusWithABrokenConfig reports the rest of the status when chalkctl cannot read the node's
// config from the inventory.
func TestStatusWithABrokenConfig(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, []byte(`{}`))
	addr := ta.startNode(t, s)
	ta.writeNode(t, strings.Replace(n1Node, "imageRef: test-metal", "imageRef: missing", 1), n1SystemDisk, n1Network)
	if err := ta.run(context.Background(), ta.args([]string{"status", "n1"}, addr)); err != nil {
		t.Fatal(err)
	}
	out := ta.stdout.String()
	if !strings.Contains(out, "config none (cannot read the config the inventory gives it: ") || !strings.Contains(out, "platform metal") || !strings.Contains(out, "var") {
		t.Errorf("status = %q", out)
	}
}

// TestAddressWithABrokenConfig reaches a node whose config chalkctl cannot read at the address
// the inventory names, so commands still reach it.
func TestAddressWithABrokenConfig(t *testing.T) {
	ta := newTestApp(t)
	ta.writeNode(t, strings.Replace(n1Node, "imageRef: test-metal", "imageRef: missing", 1), n1SystemDisk, n1Network)
	c, err := loadInventory(ta.inventory)
	if err != nil {
		t.Fatal(err)
	}
	if addr, _, err := c.address("", "n1"); err != nil || addr != "10.0.0.11" {
		t.Errorf("address = %q, %v; want the inventory's 10.0.0.11", addr, err)
	}
}

// kubernetesImage gives the node s a running image with Kubernetes: its image.json.
func kubernetesImage(t *testing.T, s *chalkd.Server) {
	t.Helper()
	s.Loader.KubernetesImage = filepath.Join(t.TempDir(), "image.json")
	writeFile(t, s.Loader.KubernetesImage, testImageJSON)
}
