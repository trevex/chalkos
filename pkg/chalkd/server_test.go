package chalkd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
)

// fakeRunner records commands and answers them from the first rule whose prefix matches.
type fakeRunner struct {
	mu    sync.Mutex
	rules []rule
	calls []string
	envs  map[string][]string
	// inputs holds what commands received on standard input, by command line.
	inputs map[string]string
	// quiet holds the command lines run without their standard error reaching the log.
	quiet map[string]bool
}

func (f *fakeRunner) RunWithInput(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	if f.inputs == nil {
		f.inputs = map[string]string{}
	}
	f.inputs[strings.Join(append([]string{name}, args...), " ")] = string(input)
	f.mu.Unlock()
	return f.RunWithEnv(ctx, nil, name, args...)
}

type rule struct {
	prefix string
	out    string
	err    error
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f.RunWithEnv(ctx, nil, name, args...)
}

func (f *fakeRunner) RunQuiet(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	if f.quiet == nil {
		f.quiet = map[string]bool{}
	}
	f.quiet[strings.Join(append([]string{name}, args...), " ")] = true
	f.mu.Unlock()
	return f.RunWithEnv(ctx, nil, name, args...)
}

func (f *fakeRunner) RunWithEnv(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	if env != nil {
		if f.envs == nil {
			f.envs = map[string][]string{}
		}
		f.envs[line] = env
	}
	for _, r := range f.rules {
		if strings.HasPrefix(line, r.prefix) {
			return []byte(r.out), r.err
		}
	}
	return nil, nil
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

type testDisk struct {
	name, devnum string
	props        map[string]string
}

var (
	vda = testDisk{"vda", "253:0", map[string]string{"ID_PATH": "pci-0000:00:04.0"}}
	vdb = testDisk{"vdb", "253:16", map[string]string{"ID_PATH": "pci-0000:00:05.0", "ID_SERIAL": "chalk-extra"}}
)

// newTestServer returns a server in the mode whose paths point at fixture trees: vda is the
// boot disk and carries VAR as vda7.
func newTestServer(t *testing.T, mode nodev1.Mode, disks ...testDisk) (*Server, *fakeRunner) {
	t.Helper()
	root := t.TempDir()
	h := storage.Host{
		SysRoot:  filepath.Join(root, "sys"),
		UdevRoot: filepath.Join(root, "udev"),
		DevRoot:  filepath.Join(root, "dev"),
	}
	for _, d := range disks {
		dir := filepath.Join(h.SysRoot, "block", d.name)
		write(t, filepath.Join(dir, "dev"), d.devnum+"\n")
		write(t, filepath.Join(dir, "size"), strconv.Itoa(16<<30/512)+"\n")
		write(t, filepath.Join(dir, "queue", "rotational"), "1\n")
		var props string
		for k, v := range d.props {
			props += "E:" + k + "=" + v + "\n"
		}
		write(t, filepath.Join(h.UdevRoot, "b"+d.devnum), props)
		write(t, filepath.Join(h.DevRoot, d.name), "")
	}
	if err := os.MkdirAll(filepath.Join(h.DevRoot, "disk", "chalk-boot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../vda", filepath.Join(h.DevRoot, "disk", "chalk-boot-disk")); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	s := &Server{
		Mode: mode,
		Paths: Paths{
			StateDir:       filepath.Join(root, "state"),
			BootDisk:       "/dev/disk/chalk-boot-disk",
			BootPartitions: "/dev/disk/chalk-boot",
			OSRelease:      filepath.Join(root, "os-release"),
			EFIVars:        filepath.Join(root, "efivars"),
			TPM:            filepath.Join(root, "tpm"),
			StorageStatus:  filepath.Join(root, "run", "storage-status.json"),
			MountInfo:      filepath.Join(root, "mountinfo"),
			ESP:            filepath.Join(root, "esp"),
			Cmdline:        filepath.Join(root, "cmdline"),
			FailedBoot:     filepath.Join(root, "var", "failed-boot"),
		},
		Run:  r,
		Host: h,
		Identity: identity.Loader{
			Identity:    filepath.Join(root, "state", "identity.json"),
			RunDir:      filepath.Join(root, "run", "chalkos"),
			NetworkDir:  filepath.Join(root, "run", "network"),
			Consumers:   filepath.Join(root, "consumers.json"),
			SetHostname: func(string) error { return nil },
		},
		InPlace:   func(context.Context, install.Request) error { return errors.New("no install expected") },
		FromParts: func(context.Context, install.PartsRequest) error { return errors.New("no install expected") },
		Journal: func(context.Context, string, bool) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("")), nil
		},
		RebootNode: func() {},
	}
	osCA, err := testOSCA()
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(s.Paths.StateDir, "chalkd", CAFile), osCA.Certificate)
	write(t, s.Paths.MountInfo, "30 1 0:27 / / rw - tmpfs tmpfs rw\n")
	write(t, s.Paths.OSRelease, "IMAGE_ID=chalkos\nIMAGE_VERSION=0.1.0\nCHALKOS_CLUSTER=lab\nCHALKOS_ROLE=worker\nCHALKOS_PLATFORM=metal\n")
	return s, r
}

// creds holds the OS CA, the node CA, client certificates of each role and the node certificate.
type creds struct {
	ca      pki.CertKey
	pool    *x509.CertPool
	clients map[string]*tls.Certificate
	nodeCA  pki.CertKey
	node    pki.CertKey
}

func newCreds(t *testing.T) creds {
	t.Helper()
	return newCredsAt(t, time.Now())
}

// newCredsAt issues the credentials at now.
func newCredsAt(t *testing.T, now time.Time) creds {
	t.Helper()
	ca, err := pki.NewOSCA(now)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := pki.ParseCertificate([]byte(ca.Certificate))
	c := creds{ca: ca, pool: x509.NewCertPool(), clients: map[string]*tls.Certificate{}}
	c.pool.AddCert(cert)
	for _, role := range []string{pki.RoleAdmin, pki.RoleOperator, pki.RoleReader} {
		ck, err := pki.IssueClient(ca, role, role, time.Hour, now)
		if err != nil {
			t.Fatal(err)
		}
		pair, _ := tls.X509KeyPair([]byte(ck.Certificate), []byte(ck.Key))
		c.clients[role] = &pair
	}
	nodeCA, err := pki.NewNodeCA(ca, now)
	if err != nil {
		t.Fatal(err)
	}
	c.nodeCA = nodeCA
	if c.node, err = pki.IssueNode(nodeCA, pki.NodeNames{CommonName: "n1", DNSNames: []string{"n1"}}, now); err != nil {
		t.Fatal(err)
	}
	pair, _ := tls.X509KeyPair([]byte(c.node.Certificate), []byte(c.node.Key))
	c.clients[pki.RoleNode] = &pair
	c.clients[unknownOrganization] = clientWithOrganization(t, ca, []string{"root"})
	c.clients[noOrganization] = clientWithOrganization(t, ca, nil)
	admin := clientWithOrganization(t, nodeCA, []string{pki.RoleAdmin})
	nodeCACert, _ := pki.ParseCertificate([]byte(nodeCA.Certificate))
	admin.Certificate = append(admin.Certificate, nodeCACert.Raw)
	c.clients[nodeCAAdmin] = admin
	return c
}

// Clients whose certificates the OS CA issued without a role.
const (
	unknownOrganization = "unknown organization"
	noOrganization      = "no organization"
	// nodeCAAdmin holds a certificate of the node CA that names the admin role.
	nodeCAAdmin = "admin organization from the node CA"
)

// deniedEverything is what a client without a role gets in normal mode.
var deniedEverything = map[string]connect.Code{
	"Info": connect.CodePermissionDenied, "Disks": connect.CodePermissionDenied, "Status": connect.CodePermissionDenied,
	"Logs": connect.CodePermissionDenied, "Reboot": connect.CodePermissionDenied, "ApplyIdentity": connect.CodePermissionDenied,
	"ResetVolume": connect.CodePermissionDenied, "Install": connect.CodeFailedPrecondition, "Bootstrap": connect.CodePermissionDenied,
	"RenewNodeCertificate": connect.CodePermissionDenied, "RotationStep": connect.CodePermissionDenied, "Upgrade": connect.CodePermissionDenied,
	"DrainNode": connect.CodePermissionDenied, "UncordonNode": connect.CodePermissionDenied,
}

// nodeRole is what a node gets in normal mode: RenewNodeCertificate alone, which a node without
// Kubernetes refuses after authorisation.
var nodeRole = map[string]connect.Code{
	"Info": connect.CodePermissionDenied, "Status": connect.CodePermissionDenied, "Reboot": connect.CodePermissionDenied,
	"ApplyIdentity": connect.CodePermissionDenied, "EtcdMembers": connect.CodePermissionDenied, "RenewNodeCertificate": connect.CodeFailedPrecondition,
	"RotationStep": connect.CodePermissionDenied, "Upgrade": connect.CodePermissionDenied, "DrainNode": connect.CodePermissionDenied,
	"UncordonNode": connect.CodePermissionDenied,
}

// clientWithOrganization issues a client certificate with the Organization given, bypassing the
// role check of pki.IssueClient.
func clientWithOrganization(t *testing.T, ca pki.CertKey, organization []string) *tls.Certificate {
	t.Helper()
	caCert, caKey, err := ca.Parse()
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "intruder", Organization: organization},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// serve starts s with its normal-mode node certificate, or a self-signed one in maintenance
// mode; clientCAs nil accepts any client.
func serve(t *testing.T, s *Server, c creds, clientCAs *x509.CertPool) string {
	t.Helper()
	cert := c.node
	if s.Mode == maintenance {
		var err error
		if cert, err = pki.SelfSigned("chalkd", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	pair, err := tls.X509KeyPair([]byte(cert.Certificate), []byte(cert.Key))
	if err != nil {
		t.Fatal(err)
	}
	s.AnyClient = clientCAs == nil
	s.ClientCAs = StaticCAs(clientCAs)
	return serveTLS(t, s.Handler(), TLSConfig(StaticCertificate(&pair), StaticCAs(clientCAs)))
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

func dial(t *testing.T, addr string, cert *tls.Certificate) *client.Conn {
	t.Helper()
	c, err := client.Dial(addr, client.Options{Insecure: true, Certificate: cert})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// call runs one RPC and returns its error code; 0 when it succeeded.
func call(c *client.Conn, procedure string) connect.Code {
	ctx := context.Background()
	var err error
	switch procedure {
	case "Info":
		_, err = c.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
	case "Status":
		_, err = c.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
	case "Reboot":
		_, err = c.Reboot(ctx, connect.NewRequest(&nodev1.RebootRequest{}))
	case "ApplyIdentity":
		_, err = c.ApplyIdentity(ctx, connect.NewRequest(&nodev1.ApplyIdentityRequest{Identity: "{}"}))
	case "ResetVolume":
		_, err = c.ResetVolume(ctx, connect.NewRequest(&nodev1.ResetVolumeRequest{Volume: "data", Identity: "{}"}))
	case "Disks":
		_, err = c.Disks(ctx, connect.NewRequest(&nodev1.DisksRequest{}))
	case "Bootstrap":
		_, err = c.Bootstrap(ctx, connect.NewRequest(&nodev1.BootstrapRequest{}))
	case "EtcdMembers":
		_, err = c.EtcdMembers(ctx, connect.NewRequest(&nodev1.EtcdMembersRequest{}))
	case "EtcdRemoveMember":
		_, err = c.EtcdRemoveMember(ctx, connect.NewRequest(&nodev1.EtcdRemoveMemberRequest{Member: "cp2"}))
	case "EtcdLeave":
		_, err = c.EtcdLeave(ctx, connect.NewRequest(&nodev1.EtcdLeaveRequest{}))
	case "RenewNodeCertificate":
		_, err = c.RenewNodeCertificate(ctx, connect.NewRequest(&nodev1.RenewNodeCertificateRequest{}))
	case "RotationStep":
		_, err = c.RotationStep(ctx, connect.NewRequest(&nodev1.RotationStepRequest{Step: nodev1.RotationStep_ROTATION_STEP_COUNT_ENCRYPTED}))
	case "Logs":
		var s *connect.ServerStreamForClient[nodev1.LogsResponse]
		if s, err = c.Logs(ctx, connect.NewRequest(&nodev1.LogsRequest{})); err == nil {
			for s.Receive() {
			}
			err = s.Err()
		}
	case "Install":
		stream := c.Install(ctx)
		stream.Send(&nodev1.InstallRequest{})
		_, err = stream.CloseAndReceive()
	case "DrainNode":
		_, err = c.DrainNode(ctx, connect.NewRequest(&nodev1.DrainNodeRequest{Node: "w1"}))
	case "UncordonNode":
		_, err = c.UncordonNode(ctx, connect.NewRequest(&nodev1.UncordonNodeRequest{Node: "w1"}))
	case "Upgrade":
		stream := c.Upgrade(ctx)
		stream.Send(&nodev1.UpgradeRequest{})
		_, err = stream.CloseAndReceive()
	}
	if err == nil {
		return 0
	}
	return connect.CodeOf(err)
}

func TestAuthorisation(t *testing.T) {
	c := newCreds(t)
	for _, tc := range []struct {
		name      string
		mode      nodev1.Mode
		clientCAs bool
		// codes holds, by role (empty: no certificate), the outcome of each procedure.
		codes map[string]map[string]connect.Code
	}{
		{"normal", normal, true, map[string]map[string]connect.Code{
			// The node has no Kubernetes: refusing EtcdMembers means the call got through.
			pki.RoleReader: {"Info": 0, "Disks": 0, "Status": 0, "Logs": 0, "Reboot": connect.CodePermissionDenied, "ApplyIdentity": connect.CodePermissionDenied, "ResetVolume": connect.CodePermissionDenied, "Install": connect.CodeFailedPrecondition,
				"EtcdMembers": connect.CodeFailedPrecondition, "EtcdRemoveMember": connect.CodePermissionDenied, "EtcdLeave": connect.CodePermissionDenied,
				"RotationStep": connect.CodePermissionDenied, "Upgrade": connect.CodePermissionDenied, "DrainNode": connect.CodePermissionDenied,
				"UncordonNode": connect.CodePermissionDenied},
			// A request without a header is refused after authorisation.
			pki.RoleOperator: {"Reboot": 0, "ApplyIdentity": connect.CodePermissionDenied, "ResetVolume": connect.CodePermissionDenied, "Bootstrap": connect.CodePermissionDenied,
				"EtcdRemoveMember": connect.CodePermissionDenied, "EtcdLeave": connect.CodePermissionDenied, "RotationStep": connect.CodePermissionDenied,
				"Upgrade": connect.CodeInvalidArgument, "DrainNode": connect.CodeFailedPrecondition, "UncordonNode": connect.CodeFailedPrecondition},
			// The identity "{}" lacks a storage section and the node has no Kubernetes; refusing them
			// means the call got through.
			pki.RoleAdmin: {"ApplyIdentity": connect.CodeInvalidArgument, "ResetVolume": connect.CodeInvalidArgument, "Reboot": 0, "Bootstrap": connect.CodeFailedPrecondition,
				"EtcdRemoveMember": connect.CodeFailedPrecondition, "EtcdLeave": connect.CodeFailedPrecondition, "RenewNodeCertificate": connect.CodePermissionDenied,
				"RotationStep": connect.CodeFailedPrecondition},
			// A certificate of the OS CA without a role's Organization grants nothing.
			unknownOrganization: deniedEverything,
			noOrganization:      deniedEverything,
			// Whatever its Organization says, a certificate of the node CA is a node's.
			nodeCAAdmin:  nodeRole,
			pki.RoleNode: nodeRole,
		}},
		{"maintenance with OS CA", maintenance, true, map[string]map[string]connect.Code{
			pki.RoleReader: {"Info": 0, "Disks": 0, "Install": connect.CodePermissionDenied, "Status": connect.CodeFailedPrecondition, "EtcdMembers": connect.CodeFailedPrecondition},
			// A header without a target is refused after authorisation.
			pki.RoleAdmin: {"Install": connect.CodeInvalidArgument, "ApplyIdentity": connect.CodeFailedPrecondition, "ResetVolume": connect.CodeFailedPrecondition, "Bootstrap": connect.CodeFailedPrecondition, "Upgrade": connect.CodeFailedPrecondition, "DrainNode": connect.CodeFailedPrecondition,
				"UncordonNode": connect.CodeFailedPrecondition},
			unknownOrganization: {"Info": connect.CodePermissionDenied, "Install": connect.CodePermissionDenied},
		}},
		{"maintenance on a generic image", maintenance, false, map[string]map[string]connect.Code{
			"": {"Info": 0, "Install": connect.CodeInvalidArgument, "Reboot": 0, "Disks": 0, "ResetVolume": connect.CodeFailedPrecondition},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestServer(t, tc.mode, vda)
			write(t, filepath.Join(s.Paths.StateDir, "identity.json"), "{}")
			write(t, filepath.Join(s.Paths.StateDir, "storage", "storage.json"), "{}")
			var pool *x509.CertPool
			if tc.clientCAs {
				pool = c.pool
			}
			addr := serve(t, s, c, pool)
			for role, codes := range tc.codes {
				conn := dial(t, addr, c.clients[role])
				for procedure, want := range codes {
					if got := call(conn, procedure); got != want {
						t.Errorf("%s as %q: code %v, want %v", procedure, role, got, want)
					}
				}
			}
		})
	}
}

func TestClientWithoutCertificateIsRejected(t *testing.T) {
	c := newCreds(t)
	for _, mode := range []nodev1.Mode{normal, maintenance} {
		s, _ := newTestServer(t, mode, vda)
		called := false
		s.Journal = func(context.Context, string, bool) (io.ReadCloser, error) {
			called = true
			return io.NopCloser(strings.NewReader("")), nil
		}
		addr := serve(t, s, c, c.pool)
		conn := dial(t, addr, nil)
		if code := call(conn, "Info"); code == 0 {
			t.Errorf("%v: a client without a certificate was served", mode)
		}
		if call(conn, "Logs"); called {
			t.Errorf("%v: a client without a certificate reached a handler", mode)
		}
	}
}

func TestInfo(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	s.Installer = true
	s.Fingerprint = "ab01"
	write(t, s.Paths.OSRelease, "ID=nixos\nIMAGE_ID=\"chalkos-installer\"\nIMAGE_VERSION=\"0.1.0\"\nCHALKOS_CLUSTER=lab\nCHALKOS_ROLE=\"worker\"\n")
	write(t, filepath.Join(s.Paths.EFIVars, "SecureBoot-"+globalVariable), "\x06\x00\x00\x00\x01")
	write(t, filepath.Join(s.Paths.EFIVars, "SetupMode-"+globalVariable), "\x06\x00\x00\x00\x00")
	write(t, filepath.Join(s.Paths.TPM, "tpm0", "dev"), "10:224\n")

	resp, err := s.Info(context.Background(), connect.NewRequest(&nodev1.InfoRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	got := resp.Msg
	if got.Mode != maintenance || got.ImageId != "chalkos-installer" || got.Version != "0.1.0" || !got.Installer ||
		got.BootDisk != "/dev/vda" || !got.Tpm || got.SecureBoot != nodev1.SecureBoot_SECURE_BOOT_ENABLED || got.Fingerprint != "ab01" || got.Cluster != "lab" || got.Role != "worker" || got.Platform != "" {
		t.Errorf("info = %+v", got)
	}
}

// TestPlatform reports the platform the running image was built for in Info and Status.
func TestPlatform(t *testing.T) {
	s, _ := installedServer(t, section("", ""), false)
	info, err := s.Info(context.Background(), connect.NewRequest(&nodev1.InfoRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Msg.Platform; got != "metal" {
		t.Errorf("Info names the platform %q, want metal", got)
	}
	if got := status(t, s).Platform; got != "metal" {
		t.Errorf("Status names the platform %q, want metal", got)
	}
}

func TestDisks(t *testing.T) {
	s, _ := newTestServer(t, normal, vda, vdb)
	dir := filepath.Join(s.Host.SysRoot, "block", "vda", "vda7")
	write(t, filepath.Join(dir, "partition"), "7\n")
	write(t, filepath.Join(dir, "dev"), "253:7\n")
	write(t, filepath.Join(dir, "size"), "2048\n")
	write(t, filepath.Join(s.Host.UdevRoot, "b253:7"), "E:ID_PART_ENTRY_NAME=var\nE:ID_FS_TYPE=crypto_LUKS\n")
	write(t, filepath.Join(s.Paths.StateDir, "storage", "disks.json"), `{"disks": {"extra": {"ref": {"serial": "chalk-extra"}, "identity": {"serial": "chalk-extra", "size": 1, "type": "hdd"}, "partitions": {}}}}`)

	resp, err := s.Disks(context.Background(), connect.NewRequest(&nodev1.DisksRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	disks := resp.Msg.Disks
	if len(disks) != 2 || disks[0].Usage != "boot" || disks[1].Usage != "disk extra" || disks[1].Serial != "chalk-extra" {
		t.Fatalf("disks = %+v", disks)
	}
	p := disks[0].Partitions
	if len(p) != 1 || p[0].Number != 7 || p[0].Label != "var" || p[0].Content != "crypto_LUKS" || p[0].Size != 1<<20 {
		t.Errorf("vda partitions = %+v", p)
	}
}

func TestStatus(t *testing.T) {
	s, r := newTestServer(t, normal, vda)
	identityJSON := `{"hostname": "n1"}`
	write(t, filepath.Join(s.Paths.StateDir, "identity.json"), identityJSON)
	write(t, filepath.Join(s.Paths.StateDir, "storage", "storage.json"), `{
	  "disks": {"system": {"ref": "/dev/vda", "seed": "s", "repart": {}}, "extra": {"ref": {"serial": "chalk-extra"}, "seed": "e", "repart": {}}},
	  "volumes": {
	    "var": {"disk": "system", "label": "var", "format": "ext4", "mountPoint": "/var", "encryption": "tpm2"},
	    "extra": {"disk": "extra", "label": "extra", "format": "ext4", "mountPoint": "/srv/extra", "encryption": "tpm2"}
	  },
	  "fallback": "recovery-key", "encryption": "tpm2"}`)
	write(t, filepath.Join(s.Paths.StateDir, "storage", "disks.json"), `{"disks": {"system": {"ref": "/dev/vda", "identity": {"path": "p", "size": 1, "type": "hdd"}, "partitions": {"var": "a"}}}}`)
	write(t, s.Paths.StorageStatus, `{"installed": true, "disks": {"system": {"device": "/dev/disk/chalk-boot-disk"}, "extra": {"error": "no disk matches serial \"chalk-extra\""}}}`)
	write(t, s.Paths.MountInfo, "30 1 0:27 / / rw - tmpfs tmpfs rw\n41 30 253:1 / /var rw - ext4 /dev/mapper/var rw\n")
	r.rules = []rule{{prefix: "systemctl list-units --state=failed", out: "systemd-cryptsetup@extra.service loaded failed failed Unlock chalkos volume extra\nsrv-extra.mount loaded failed failed chalkos volume extra\n"}}

	resp, err := s.Status(context.Background(), connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	got := resp.Msg
	if got.IdentityVersion != identity.IdentityVersion([]byte(identityJSON)) {
		t.Errorf("identity version = %s", got.IdentityVersion)
	}
	if !reflect.DeepEqual(got.FailedUnits, []string{"systemd-cryptsetup@extra.service", "srv-extra.mount"}) {
		t.Errorf("failed units = %v", got.FailedUnits)
	}
	if len(got.Disks) != 2 || got.Disks[0].Name != "extra" || !strings.Contains(got.Disks[0].Error, "no disk matches") {
		t.Errorf("disks = %+v", got.Disks)
	}
	if len(got.Volumes) != 2 || got.Volumes[0].Name != "extra" || got.Volumes[0].Present || got.Volumes[0].Mounted ||
		got.Volumes[1].Name != "var" || !got.Volumes[1].Present || !got.Volumes[1].Mounted {
		t.Errorf("volumes = %+v", got.Volumes)
	}
}

func TestLogsStreamsJournal(t *testing.T) {
	c := newCreds(t)
	s, _ := newTestServer(t, normal, vda)
	var unit string
	var follow bool
	s.Journal = func(_ context.Context, u string, f bool) (io.ReadCloser, error) {
		unit, follow = u, f
		return io.NopCloser(strings.NewReader("line one\nline two\n")), nil
	}
	conn := dial(t, serve(t, s, c, c.pool), c.clients[pki.RoleReader])
	stream, err := conn.Logs(context.Background(), connect.NewRequest(&nodev1.LogsRequest{Unit: "chalkd.service", Follow: true}))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for stream.Receive() {
		lines = append(lines, stream.Msg().Line)
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lines, []string{"line one", "line two"}) || unit != "chalkd.service" || !follow {
		t.Errorf("lines = %v, unit = %q, follow = %v", lines, unit, follow)
	}
}

func TestRebootAfterResponse(t *testing.T) {
	s, _ := newTestServer(t, normal, vda)
	rebooted := make(chan struct{})
	s.RebootNode = func() { close(rebooted) }
	if _, err := s.Reboot(context.Background(), connect.NewRequest(&nodev1.RebootRequest{})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-rebooted:
	case <-time.After(time.Minute):
		t.Fatal("the node did not reboot")
	}
}

// A procedure without an entry is refused, so a new one must be given its modes and role.
func TestEveryProcedureHasPermissions(t *testing.T) {
	methods := nodev1.File_chalkos_node_v1_node_proto.Services().ByName("NodeService").Methods()
	if methods.Len() == 0 {
		t.Fatal("the service has no methods")
	}
	for i := 0; i < methods.Len(); i++ {
		procedure := "/" + nodev1connect.NodeServiceName + "/" + string(methods.Get(i).Name())
		if _, ok := permissions[procedure]; !ok {
			t.Errorf("%s has no entry in permissions", procedure)
		}
	}
	if len(permissions) != methods.Len() {
		t.Errorf("permissions has %d entries for %d procedures", len(permissions), methods.Len())
	}
}

func TestRequestSizeLimit(t *testing.T) {
	c := newCreds(t)
	s, r := installedServer(t, section("", ""), false)
	conn := dial(t, serve(t, s, c, c.pool), c.clients[pki.RoleAdmin])
	ctx := context.Background()

	large := strings.Repeat("x", maxMessageBytes+1)
	_, err := conn.ApplyIdentity(ctx, connect.NewRequest(&nodev1.ApplyIdentityRequest{Identity: large}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("err = %v, want resource exhausted", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %v", r.calls)
	}
	// Below the limit, the request reaches the handler, which refuses what is not an identity.
	_, err = conn.ApplyIdentity(ctx, connect.NewRequest(&nodev1.ApplyIdentityRequest{Identity: large[:maxMessageBytes-1024]}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("err = %v, want the handler's refusal", err)
	}
	if code := call(conn, "Info"); code != 0 {
		t.Errorf("Info after an oversized request: %v", code)
	}
}

// An install message carries a chunk of the image well below the limit.
func TestImageChunkFitsRequestLimit(t *testing.T) {
	const chunkSize = 1 << 20
	if maxMessageBytes < 2*chunkSize {
		t.Errorf("maxMessageBytes = %d leaves no room for a %d-byte image chunk", maxMessageBytes, chunkSize)
	}
}

// rawClient is a NodeServiceClient with its own connect options, so a test can send compressed
// requests, which client.Conn does not support.
func rawClient(t *testing.T, addr string, opts ...connect.ClientOption) nodev1connect.NodeServiceClient {
	t.Helper()
	transport := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		ForceAttemptHTTP2: true,
	}
	t.Cleanup(transport.CloseIdleConnections)
	return nodev1connect.NewNodeServiceClient(&http.Client{Transport: transport}, "https://"+addr, opts...)
}

// TestCompressionRefused checks that a gzip-compressed request is refused before chalkd spends
// CPU decompressing it, and that the server does not advertise gzip support in return.
func TestCompressionRefused(t *testing.T) {
	c := newCreds(t)
	s, _ := newTestServer(t, maintenance, vda)
	addr := serve(t, s, c, nil)

	gzip := rawClient(t, addr, connect.WithSendGzip())
	if _, err := gzip.Info(context.Background(), connect.NewRequest(&nodev1.InfoRequest{})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("gzip request: err = %v, want CodeUnimplemented", err)
	}

	plain := rawClient(t, addr)
	resp, err := plain.Info(context.Background(), connect.NewRequest(&nodev1.InfoRequest{}))
	if err != nil {
		t.Fatalf("uncompressed request: %v", err)
	}
	if ae := resp.Header().Get("Accept-Encoding"); strings.Contains(ae, "gzip") {
		t.Errorf("server advertised compression: %q", ae)
	}
}

func TestLogsLimitsStreams(t *testing.T) {
	c := newCreds(t)
	s, _ := newTestServer(t, normal, vda)
	started := make(chan struct{}, maxLogStreams+2)
	s.Journal = func(ctx context.Context, _ string, _ bool) (io.ReadCloser, error) {
		started <- struct{}{}
		pr, pw := io.Pipe()
		go func() {
			<-ctx.Done()
			pw.Close()
		}()
		return pr, nil
	}
	conn := dial(t, serve(t, s, c, c.pool), c.clients[pki.RoleReader])
	// follow opens a stream and reports how it ended, once it does.
	follow := func(ctx context.Context) <-chan error {
		done := make(chan error, 1)
		go func() {
			stream, err := conn.Logs(ctx, connect.NewRequest(&nodev1.LogsRequest{Follow: true}))
			if err == nil {
				for stream.Receive() {
				}
				err = stream.Err()
			}
			done <- err
		}()
		return done
	}
	waitStarted := func() {
		t.Helper()
		select {
		case <-started:
		case <-time.After(time.Minute):
			t.Fatal("a stream did not reach the journal")
		}
	}

	var cancels []context.CancelFunc
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()
	for range maxLogStreams {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		follow(ctx)
		waitStarted()
	}
	over, cancelOver := context.WithCancel(context.Background())
	cancels = append(cancels, cancelOver)
	select {
	case err := <-follow(over):
		if connect.CodeOf(err) != connect.CodeResourceExhausted {
			t.Fatalf("err = %v, want resource exhausted", err)
		}
	case <-started:
		t.Fatalf("stream %d reached the journal", maxLogStreams+1)
	case <-time.After(time.Minute):
		t.Fatal("the stream over the limit was not refused")
	}

	// A stream that ends frees its slot.
	cancels[0]()
	deadline := time.Now().Add(time.Minute)
	for {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		done := follow(ctx)
		select {
		case <-started:
			return
		case err := <-done:
			if connect.CodeOf(err) != connect.CodeResourceExhausted || time.Now().After(deadline) {
				t.Fatalf("err = %v after a stream ended", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
