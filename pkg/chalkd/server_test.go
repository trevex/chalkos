package chalkd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/api/router/v1/routerv1connect"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/chalkd/layer/layertest"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/effects"
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/nodeconfig"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage/storagetest"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// systemPin and extraPin are the disks.json entries that pin vda as the disk system and vdb as
// extra, with their partitions' PARTUUIDs by name.
func systemPin(partitions map[string]string) string {
	return `"system": {"ref": "/dev/vda", "identity": {"path": "pci-0000:00:04.0", "size": 17179869184, "type": "hdd"}, "partitions": ` + partitionsJSON(partitions) + `}`
}

func extraPin(partitions map[string]string) string {
	return `"extra": {"ref": {"serial": "chalk-extra"}, "identity": {"serial": "chalk-extra", "path": "pci-0000:00:05.0", "size": 17179869184, "type": "hdd"}, "partitions": ` + partitionsJSON(partitions) + `}`
}

func partitionsJSON(partitions map[string]string) string {
	if partitions == nil {
		return "{}"
	}
	data, err := json.Marshal(partitions)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// writePins writes the server's disks.json with the pins.
func writePins(t *testing.T, s *Server, pins ...string) {
	t.Helper()
	write(t, filepath.Join(s.Paths.StateDir, "storage", "disks.json"), `{"disks": {`+strings.Join(pins, ", ")+`}}`)
}

var (
	vda = storagetest.Disk{Name: "vda", Devnum: "253:0", Props: map[string]string{"ID_PATH": "pci-0000:00:04.0"}}
	vdb = storagetest.Disk{Name: "vdb", Devnum: "253:16", Props: map[string]string{"ID_PATH": "pci-0000:00:05.0", "ID_SERIAL": "chalk-extra"}}
)

// newTestServer returns a server in the mode whose paths point at fixture trees: vda is the
// boot disk and carries VAR as vda7.
func newTestServer(t *testing.T, mode nodev1.Mode, disks ...storagetest.Disk) (*Server, *tooltest.Runner) {
	t.Helper()
	root := t.TempDir()
	h := storagetest.NewHost(t, "vda", disks...)
	if err := os.MkdirAll(filepath.Join(h.DevRoot, "disk", "chalk-boot"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &tooltest.Runner{}
	s := &Server{
		Mode: mode,
		Paths: Paths{
			StateDir:        filepath.Join(root, "state"),
			BootDisk:        "/dev/disk/chalk-boot-disk",
			BootPartitions:  "/dev/disk/chalk-boot",
			OSRelease:       filepath.Join(root, "os-release"),
			EFIVars:         filepath.Join(root, "efivars"),
			TPM:             filepath.Join(root, "tpm"),
			StorageStatus:   filepath.Join(root, "run", "storage-status.json"),
			MountInfo:       filepath.Join(root, "mountinfo"),
			ESP:             filepath.Join(root, "esp"),
			Cmdline:         filepath.Join(root, "cmdline"),
			FailedBoot:      filepath.Join(root, "var", "failed-boot"),
			Facts:           filepath.Join(root, "facts.json"),
			Diagnostics:     filepath.Join(root, "diagnostics.json"),
			EffectsBaseline: filepath.Join(root, "run", "chalkd", "effects-baseline.json"),
		},
		Run:  r,
		Host: h,
		Loader: nodeconfig.Loader{
			RunDir:          filepath.Join(root, "run", "chalkos"),
			Services:        filepath.Join(root, "services.json"),
			Effects:         filepath.Join(root, "effects.json"),
			Outputs:         filepath.Join(root, "outputs.json"),
			Render:          outputs.Renderer{Root: filepath.Join(root, "run", "chalkos", "outputs"), Run: outputs.Direct{}},
			NodeCertificate: filepath.Join(root, "state", "chalkd", NodeCertificateFile),
			Slots:           filepath.Join(root, "state", "config"),
			Cmdline:         filepath.Join(root, "cmdline"),
			Schemas:         filepath.Join(root, "schemas"),
			SetHostname:     func(string) error { return nil },
		},
		InPlace:   func(context.Context, install.Request, []byte) error { return errors.New("no install expected") },
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
	write(t, s.Paths.Cmdline, "init=/x usrhash="+runningRoot+"\n")
	write(t, s.Paths.OSRelease, "IMAGE_ID=chalkos\nIMAGE_VERSION=\"\"\nCHALKOS_VARIANT=worker\nCHALKOS_PLATFORM=metal\n")
	writeSchemas(t, s.Loader.Schemas)
	s.Facts = storageFacts{s}
	writeFacts(t, s)
	writeServices(t, s, "{}")
	writeEffects(t, s, "{}")
	withPlugins(t, s, layers.Of(), nil)
	return s, r
}

// withPlugins compiles the Kubernetes and router layers into s, with s's Kubernetes side and the
// router side r, or one that runs s's programs when r is nil, on an image of the layers l.
func withPlugins(t *testing.T, s *Server, l layers.Set, r *Router) {
	t.Helper()
	if r == nil {
		r = &Router{Run: s.Run}
	}
	reg, err := layer.NewRegistry(l, NewKubernetesLayer(s, s.Kubernetes), NewRouterLayer(r))
	if err != nil {
		t.Fatal(err)
	}
	s.Plugins = reg
}

// writeEffects declares the effects of the default image, which the tests carry out, and those
// of extra, a JSON object of effects by name, at the test server's path. An effect of the default
// image that runs an action of a layer s's image does not carry is left out, as an image without
// the layer has none; addLayerEffects adds it once s has the layer.
func writeEffects(t *testing.T, s *Server, extra string) {
	t.Helper()
	writeMerged(t, s.Loader.Effects, "../../test/fixtures/kinds/effects.json", extra, func(raw json.RawMessage) bool {
		return ofActiveLayer(t, s, raw)
	})
}

// addLayerEffects adds the default image's effects that run an action of a layer s's image
// carries, which writeEffects left out before s had the layer, to the effects s declares.
func addLayerEffects(t *testing.T, s *Server) {
	t.Helper()
	var fixture, declared map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFile(t, "../../test/fixtures/kinds/effects.json")), &fixture); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(readFile(t, s.Loader.Effects)), &declared); err != nil {
		t.Fatal(err)
	}
	for name, raw := range fixture {
		if _, ok := declared[name]; !ok && ofActiveLayer(t, s, raw) {
			declared[name] = raw
		}
	}
	data, err := json.Marshal(declared)
	if err != nil {
		t.Fatal(err)
	}
	write(t, s.Loader.Effects, string(data))
}

// ofActiveLayer tells whether the effect raw runs no layer action or one of a layer s's image
// carries. An action that layer lacks stays, so the delivery refuses it.
func ofActiveLayer(t *testing.T, s *Server, raw json.RawMessage) bool {
	t.Helper()
	var e effects.Definition
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	if e.Chalkd == "" {
		return true
	}
	name, _, _ := strings.Cut(e.Chalkd, ":")
	return s.Plugins.Layers().Has(name)
}

// writeServices grants the kinds the default image's units read, and those of extra, a JSON object
// of grants by unit, at the test server's path.
func writeServices(t *testing.T, s *Server, extra string) {
	t.Helper()
	writeMerged(t, s.Loader.Services, "../../test/fixtures/kinds/services.json", extra, nil)
}

// writeMerged writes the JSON object of the fixture with the keys of extra added to path; with
// keep, only the fixture's values keep holds for.
func writeMerged(t *testing.T, path, fixture, extra string, keep func(json.RawMessage) bool) {
	t.Helper()
	var base, more map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFile(t, fixture)), &base); err != nil {
		t.Fatal(err)
	}
	if keep != nil {
		maps.DeleteFunc(base, func(_ string, v json.RawMessage) bool { return !keep(v) })
	}
	if err := json.Unmarshal([]byte(extra), &more); err != nil {
		t.Fatal(err)
	}
	maps.Copy(base, more)
	data, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(data))
}

// storageFacts runs the commands of the storage facts in the test, as chalkos-storage facts would,
// against the test server's disks and with its runner.
type storageFacts struct{ s *Server }

func (f storageFacts) Run(ctx context.Context, argv []string, stdin []byte, _ int) ([]byte, error) {
	if len(argv) != 3 || argv[0] != "chalkos-storage" || argv[1] != "facts" {
		return nil, fmt.Errorf("the test runs no command %v", argv)
	}
	return f.s.storage().Facts(ctx, argv[2], stdin)
}

// writeFacts declares the facts of the built-in kinds' rules and effects as an image does, at
// the test server's paths; the cluster file only where the server writes the one it reads.
func writeFacts(t *testing.T, s *Server) {
	t.Helper()
	defined := map[string]facts.Definition{
		"share":    {File: filepath.Join(s.Paths.StateDir, "kubernetes", "share.json"), Fields: []string{"kind"}, HashOthers: true},
		"recorded": {File: filepath.Join(s.Paths.StateDir, "storage", "storage.json")},
		"disks":    {Command: "chalkos-storage facts disks"},
		"fallback": {Command: "chalkos-storage facts fallback", Inputs: []string{facts.FallbackSecret}},
	}
	if k := s.Kubernetes; k != nil && k.clusterFile == k.Paths.Cluster {
		defined["cluster"] = facts.Definition{File: k.clusterFile}
	}
	defs, err := json.Marshal(defined)
	if err != nil {
		t.Fatal(err)
	}
	write(t, s.Paths.Facts, string(defs))
}

// runningRoot is the root hash of the store the test server's node runs, as its kernel command
// line names it.
var runningRoot = strings.Repeat("ab", 32)

// slotOf is the running image's config slot.
func slotOf(t *testing.T, s *Server) string {
	t.Helper()
	dir, err := s.Loader.Slot()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeIdentity records identity in the running image's slot, keeping the documents it holds.
func writeIdentity(t *testing.T, s *Server, identity string) {
	t.Helper()
	dir := slotOf(t, s)
	stored, _, err := slot.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	stored.Identity = []byte(identity)
	if err := slot.Write(dir, stored); err != nil {
		t.Fatal(err)
	}
}

// readIdentity is the identity the running image's slot records.
func readIdentity(s *Server) ([]byte, error) {
	dir, err := s.Loader.Slot()
	if err != nil {
		return nil, err
	}
	stored, found, err := slot.Read(dir)
	if err == nil && !found {
		err = fs.ErrNotExist
	}
	return stored.Identity, err
}

// identityOf is the identity the running image's slot records, which must be there.
func identityOf(t *testing.T, s *Server) string {
	t.Helper()
	data, err := readIdentity(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// creds holds the OS CA, the node CA, client certificates of each role and the node certificate.
type creds struct {
	ca      pki.CertKey
	pool    *x509.CertPool
	clients map[pki.Role]*tls.Certificate
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
	c := creds{ca: ca, pool: x509.NewCertPool(), clients: map[pki.Role]*tls.Certificate{}}
	c.pool.AddCert(cert)
	for _, role := range []pki.Role{pki.RoleAdmin, pki.RoleOperator, pki.RoleReader} {
		ck, err := pki.IssueClient(ca, string(role), role, time.Hour, now)
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
	admin := clientWithOrganization(t, nodeCA, []string{string(pki.RoleAdmin)})
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

// deniedEverything is what a client without a role gets in normal mode; the RPCs of the
// Kubernetes layer, which the test image lacks, are unimplemented before any role is checked.
var deniedEverything = map[string]connect.Code{
	"Info": connect.CodePermissionDenied, "Disks": connect.CodePermissionDenied, "Contract": connect.CodePermissionDenied, "Status": connect.CodePermissionDenied,
	"Logs": connect.CodePermissionDenied, "Reboot": connect.CodePermissionDenied, "ApplyConfig": connect.CodePermissionDenied,
	"ResetVolume": connect.CodePermissionDenied, "Install": connect.CodeFailedPrecondition, "Bootstrap": connect.CodeUnimplemented,
	"RenewNodeCertificate": connect.CodeUnimplemented, "RotationStep": connect.CodeUnimplemented, "Upgrade": connect.CodePermissionDenied,
	"DrainNode": connect.CodeUnimplemented, "UncordonNode": connect.CodeUnimplemented, "GetConfig": connect.CodePermissionDenied,
	"RevokeNode": connect.CodeUnimplemented, "UnrevokeNode": connect.CodeUnimplemented, "Diagnostics": connect.CodePermissionDenied,
	"RunDiagnostic": connect.CodePermissionDenied, "KubernetesStatus": connect.CodeUnimplemented, "RouterStatus": connect.CodeUnimplemented,
}

// nodeRole is what a node gets in normal mode on an image without layers: nothing of the node
// API, and the unimplemented RPCs of the layers, RenewNodeCertificate among them.
var nodeRole = map[string]connect.Code{
	"Info": connect.CodePermissionDenied, "Contract": connect.CodePermissionDenied, "Status": connect.CodePermissionDenied, "Reboot": connect.CodePermissionDenied,
	"ApplyConfig": connect.CodePermissionDenied, "EtcdMembers": connect.CodeUnimplemented, "RenewNodeCertificate": connect.CodeUnimplemented,
	"RotationStep": connect.CodeUnimplemented, "Upgrade": connect.CodePermissionDenied, "UpgradeWithConfig": connect.CodePermissionDenied, "DrainNode": connect.CodeUnimplemented,
	"UncordonNode": connect.CodeUnimplemented, "GetConfig": connect.CodePermissionDenied, "RevokeNode": connect.CodeUnimplemented,
	"UnrevokeNode": connect.CodeUnimplemented, "Diagnostics": connect.CodePermissionDenied,
	"RunDiagnostic": connect.CodePermissionDenied, "KubernetesStatus": connect.CodeUnimplemented, "RouterStatus": connect.CodeUnimplemented,
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
	c, err := client.Dial(addr, client.Options{Verify: client.AnyCertificate(), Certificate: cert})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// call runs one RPC and returns its error code; 0 when it succeeded.
func call(c *client.Conn, procedure string) connect.Code {
	ctx := context.Background()
	k := kubernetesv1connect.NewKubernetesServiceClient(c.HTTPClient(), c.BaseURL())
	rt := routerv1connect.NewRouterServiceClient(c.HTTPClient(), c.BaseURL())
	var err error
	switch procedure {
	case "Info":
		_, err = c.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
	case "Status":
		_, err = c.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
	case "Reboot":
		_, err = c.Reboot(ctx, connect.NewRequest(&nodev1.RebootRequest{}))
	case "ApplyConfig":
		_, err = c.ApplyConfig(ctx, connect.NewRequest(&nodev1.ApplyConfigRequest{}))
	case "ResetVolume":
		_, err = c.ResetVolume(ctx, connect.NewRequest(&nodev1.ResetVolumeRequest{Volume: "data"}))
	case "Contract":
		_, err = c.Contract(ctx, connect.NewRequest(&nodev1.ContractRequest{}))
	case "Disks":
		_, err = c.Disks(ctx, connect.NewRequest(&nodev1.DisksRequest{}))
	case "Bootstrap":
		_, err = k.Bootstrap(ctx, connect.NewRequest(&kubernetesv1.BootstrapRequest{}))
	case "EtcdMembers":
		_, err = k.EtcdMembers(ctx, connect.NewRequest(&kubernetesv1.EtcdMembersRequest{}))
	case "EtcdRemoveMember":
		_, err = k.EtcdRemoveMember(ctx, connect.NewRequest(&kubernetesv1.EtcdRemoveMemberRequest{Member: "cp2"}))
	case "EtcdLeave":
		_, err = k.EtcdLeave(ctx, connect.NewRequest(&kubernetesv1.EtcdLeaveRequest{}))
	case "RenewNodeCertificate":
		_, err = k.RenewNodeCertificate(ctx, connect.NewRequest(&kubernetesv1.RenewNodeCertificateRequest{}))
	case "RotationStep":
		_, err = k.RotationStep(ctx, connect.NewRequest(&kubernetesv1.RotationStepRequest{Step: kubernetesv1.RotationStep_ROTATION_STEP_COUNT_ENCRYPTED}))
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
		_, err = k.DrainNode(ctx, connect.NewRequest(&kubernetesv1.DrainNodeRequest{Node: "w1"}))
	case "UncordonNode":
		_, err = k.UncordonNode(ctx, connect.NewRequest(&kubernetesv1.UncordonNodeRequest{Node: "w1"}))
	case "RevokeNode":
		_, err = k.RevokeNode(ctx, connect.NewRequest(&kubernetesv1.RevokeNodeRequest{Node: "w1"}))
	case "UnrevokeNode":
		_, err = k.UnrevokeNode(ctx, connect.NewRequest(&kubernetesv1.UnrevokeNodeRequest{Node: "w1"}))
	case "Diagnostics":
		_, err = c.Diagnostics(ctx, connect.NewRequest(&nodev1.DiagnosticsRequest{}))
	case "RunDiagnostic":
		// A diagnostic the test image lacks: refused after authorisation.
		var s *connect.ServerStreamForClient[nodev1.RunDiagnosticResponse]
		if s, err = c.RunDiagnostic(ctx, connect.NewRequest(&nodev1.RunDiagnosticRequest{Name: "nope"})); err == nil {
			for s.Receive() {
			}
			err = s.Err()
		}
	case "Routes":
		_, err = rt.Routes(ctx, connect.NewRequest(&routerv1.RoutesRequest{}))
	case "KubernetesStatus":
		_, err = k.Status(ctx, connect.NewRequest(&kubernetesv1.StatusRequest{}))
	case "RouterStatus":
		_, err = rt.Status(ctx, connect.NewRequest(&routerv1.StatusRequest{}))
	case "GetConfig":
		_, err = c.GetConfig(ctx, connect.NewRequest(&nodev1.GetConfigRequest{}))
	case "UpgradeWithConfig":
		// A header with config and no image: refused for its role before it is read further.
		stream := c.Upgrade(ctx)
		stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: &nodev1.UpgradeHeader{Config: "kind: Node"}}})
		_, err = stream.CloseAndReceive()
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
		codes map[pki.Role]map[string]connect.Code
	}{
		{"normal", normal, true, map[pki.Role]map[string]connect.Code{
			// The test image has no layers, whose RPCs are unimplemented for every role.
			pki.RoleReader: {"Info": 0, "Disks": 0, "Contract": 0, "Status": 0, "Logs": 0, "Diagnostics": 0, "RunDiagnostic": connect.CodeNotFound, "Reboot": connect.CodePermissionDenied, "ApplyConfig": connect.CodePermissionDenied, "ResetVolume": connect.CodePermissionDenied, "Install": connect.CodeFailedPrecondition,
				"EtcdMembers": connect.CodeUnimplemented, "EtcdRemoveMember": connect.CodeUnimplemented, "EtcdLeave": connect.CodeUnimplemented,
				"RotationStep": connect.CodeUnimplemented, "Upgrade": connect.CodePermissionDenied, "UpgradeWithConfig": connect.CodePermissionDenied, "DrainNode": connect.CodeUnimplemented,
				"UncordonNode": connect.CodeUnimplemented, "GetConfig": connect.CodePermissionDenied, "RevokeNode": connect.CodeUnimplemented, "UnrevokeNode": connect.CodeUnimplemented,
				"KubernetesStatus": connect.CodeUnimplemented, "RouterStatus": connect.CodeUnimplemented, "Routes": connect.CodeUnimplemented},
			// A request without a header is refused after authorisation, an upgrade with config for
			// the role.
			pki.RoleOperator: {"Reboot": 0, "ApplyConfig": connect.CodePermissionDenied, "ResetVolume": connect.CodePermissionDenied, "Bootstrap": connect.CodeUnimplemented,
				"EtcdRemoveMember": connect.CodeUnimplemented, "EtcdLeave": connect.CodeUnimplemented, "RotationStep": connect.CodeUnimplemented,
				"Upgrade": connect.CodeInvalidArgument, "UpgradeWithConfig": connect.CodePermissionDenied, "DrainNode": connect.CodeUnimplemented, "UncordonNode": connect.CodeUnimplemented,
				"GetConfig": connect.CodePermissionDenied, "RevokeNode": connect.CodeUnimplemented, "UnrevokeNode": connect.CodeUnimplemented},
			// An empty request and a reset without config are refused after authorisation; refusing
			// them means the call got through.
			pki.RoleAdmin: {"ApplyConfig": connect.CodeInvalidArgument, "ResetVolume": connect.CodeInvalidArgument, "Reboot": 0, "Bootstrap": connect.CodeUnimplemented,
				"EtcdRemoveMember": connect.CodeUnimplemented, "EtcdLeave": connect.CodeUnimplemented, "RenewNodeCertificate": connect.CodeUnimplemented,
				"RotationStep": connect.CodeUnimplemented, "GetConfig": 0, "UpgradeWithConfig": connect.CodeInvalidArgument, "RevokeNode": connect.CodeUnimplemented,
				"UnrevokeNode": connect.CodeUnimplemented},
			// A certificate of the OS CA without a role's Organization grants nothing.
			unknownOrganization: deniedEverything,
			noOrganization:      deniedEverything,
			// Whatever its Organization says, a certificate of the node CA is a node's.
			nodeCAAdmin:  nodeRole,
			pki.RoleNode: nodeRole,
		}},
		{"maintenance with OS CA", maintenance, true, map[pki.Role]map[string]connect.Code{
			pki.RoleReader: {"Info": 0, "Disks": 0, "Contract": 0, "Diagnostics": 0, "RunDiagnostic": connect.CodeNotFound, "Install": connect.CodePermissionDenied, "Status": connect.CodeFailedPrecondition, "EtcdMembers": connect.CodeUnimplemented},
			// A header without a target is refused after authorisation.
			pki.RoleAdmin: {"Install": connect.CodeInvalidArgument, "ApplyConfig": connect.CodeFailedPrecondition, "ResetVolume": connect.CodeFailedPrecondition, "Bootstrap": connect.CodeUnimplemented, "Upgrade": connect.CodeFailedPrecondition, "DrainNode": connect.CodeUnimplemented,
				"UncordonNode": connect.CodeUnimplemented, "GetConfig": connect.CodeFailedPrecondition, "RevokeNode": connect.CodeUnimplemented, "UnrevokeNode": connect.CodeUnimplemented},
			unknownOrganization: {"Info": connect.CodePermissionDenied, "Install": connect.CodePermissionDenied},
		}},
		{"maintenance on a generic image", maintenance, false, map[pki.Role]map[string]connect.Code{
			"": {"Info": 0, "Diagnostics": 0, "RunDiagnostic": connect.CodeNotFound, "Install": connect.CodeInvalidArgument, "Reboot": 0, "Disks": 0, "Contract": 0, "ResetVolume": connect.CodeFailedPrecondition},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestServer(t, tc.mode, vda)
			writeIdentity(t, s, "{}")
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
	write(t, s.Paths.OSRelease, "ID=nixos\nIMAGE_ID=\"chalkos-installer\"\nIMAGE_VERSION=\"\"\nCHALKOS_VARIANT=\"worker\"\n")
	write(t, filepath.Join(s.Paths.EFIVars, "SecureBoot-"+globalVariable), "\x06\x00\x00\x00\x01")
	write(t, filepath.Join(s.Paths.EFIVars, "SetupMode-"+globalVariable), "\x06\x00\x00\x00\x00")
	write(t, filepath.Join(s.Paths.TPM, "tpm0", "dev"), "10:224\n")
	write(t, s.Paths.Cmdline, "init=/x usrhash="+strings.Repeat("ab", 32)+"\n")

	resp, err := s.Info(context.Background(), connect.NewRequest(&nodev1.InfoRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	got := resp.Msg
	if got.Mode != maintenance || got.ImageId != "chalkos-installer" || !got.Installer || !bytes.Equal(got.RootHash, bytes.Repeat([]byte{0xab}, 32)) || got.Revision != "" || got.Tag != "" ||
		got.BootDisk != "/dev/vda" || !got.Tpm || got.SecureBoot != nodev1.SecureBoot_SECURE_BOOT_ENABLED || got.Fingerprint != "ab01" || got.Variant != "worker" || got.Platform != "" {
		t.Errorf("info = %+v", got)
	}

	// The record of installed images names the running image's provenance.
	write(t, filepath.Join(s.Paths.StateDir, "chalkd", "images.json"), `{"`+strings.Repeat("ab", 32)+`": {"revision": "a1b2c3d4e5f6", "tag": "v1.5.0"}}`)
	if resp, err = s.Info(context.Background(), connect.NewRequest(&nodev1.InfoRequest{})); err != nil || resp.Msg.Revision != "a1b2c3d4e5f6" || resp.Msg.Tag != "v1.5.0" {
		t.Errorf("info with the record = %+v, %v", resp.Msg, err)
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
	writePins(t, s, `"extra": {"ref": {"serial": "chalk-extra"}, "identity": {"serial": "chalk-extra", "size": 1, "type": "hdd"}, "partitions": {}}`)

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
	writeIdentity(t, s, identityJSON)
	write(t, filepath.Join(s.Paths.StateDir, "storage", "storage.json"), `{
	  "disks": {"system": {"ref": "/dev/vda", "seed": "s", "repart": {}}, "extra": {"ref": {"serial": "chalk-extra"}, "seed": "e", "repart": {}}},
	  "volumes": {
	    "var": {"disk": "system", "label": "var", "format": "ext4", "mountPoint": "/var", "encryption": "tpm2"},
	    "extra": {"disk": "extra", "label": "extra", "format": "ext4", "mountPoint": "/srv/extra", "encryption": "tpm2"}
	  },
	  "fallback": "recovery-key", "encryption": "tpm2"}`)
	writePins(t, s, `"system": {"ref": "/dev/vda", "identity": {"path": "p", "size": 1, "type": "hdd"}, "partitions": {"var": "a"}}`)
	write(t, s.Paths.StorageStatus, `{"installed": true, "disks": {"system": {"device": "/dev/disk/chalk-boot-disk"}, "extra": {"error": "no disk matches serial \"chalk-extra\""}}}`)
	write(t, s.Paths.MountInfo, "30 1 0:27 / / rw - tmpfs tmpfs rw\n41 30 253:1 / /var rw - ext4 /dev/mapper/var rw\n")
	r.Rules = []tooltest.Rule{{Prefix: "systemctl list-units --state=failed", Out: "systemd-cryptsetup@extra.service loaded failed failed Unlock chalkos volume extra\nsrv-extra.mount loaded failed failed chalkos volume extra\n"}}

	resp, err := s.Status(context.Background(), connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	got := resp.Msg
	if got.ConfigVersion != "" {
		t.Errorf("config version of a node without config = %q", got.ConfigVersion)
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

// A procedure without an entry is refused, so a new one must be given its modes and role; each
// RPC's comment in node.proto, which the API reference shows, names them.
func TestNodeAPIPermissions(t *testing.T) {
	service := nodev1.File_chalkos_node_v1_node_proto.Services().ByName("NodeService")
	layertest.CheckPermissions(t, service, permissions)
	layertest.CheckComments(t, filepath.Join("..", "..", "api", "chalkos", "node", "v1", "node.proto"), service, permissions, "")
}

// TestLayerAPIPermissions holds the Kubernetes and router layers' APIs to their plugins'
// permissions as TestNodeAPIPermissions holds the node API.
func TestLayerAPIPermissions(t *testing.T) {
	k := NewKubernetesLayer(nil, nil)
	service := kubernetesv1.File_chalkos_kubernetes_v1_kubernetes_proto.Services().ByName("KubernetesService")
	layertest.CheckPermissions(t, service, k.Permissions())
	layertest.CheckComments(t, filepath.Join("..", "..", "api", "chalkos", "kubernetes", "v1", "kubernetes.proto"), service, k.Permissions(), layers.Kubernetes)
	r := NewRouterLayer(nil)
	service = routerv1.File_chalkos_router_v1_router_proto.Services().ByName("RouterService")
	layertest.CheckPermissions(t, service, r.Permissions())
	layertest.CheckComments(t, filepath.Join("..", "..", "api", "chalkos", "router", "v1", "router.proto"), service, r.Permissions(), layers.Router)
}

func TestRequestSizeLimit(t *testing.T) {
	c := newCreds(t)
	s, r := installedServer(t, section("", ""), false)
	conn := dial(t, serve(t, s, c, c.pool), c.clients[pki.RoleAdmin])
	ctx := context.Background()

	large := strings.Repeat("x", maxMessageBytes+1)
	_, err := conn.ApplyConfig(ctx, connect.NewRequest(&nodev1.ApplyConfigRequest{Config: large}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("err = %v, want resource exhausted", err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("ran %v", r.Calls)
	}
	// Below the limit, the request reaches the handler, which refuses what is not config.
	_, err = conn.ApplyConfig(ctx, connect.NewRequest(&nodev1.ApplyConfigRequest{Config: large[:maxMessageBytes-1024]}))
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

func TestCount(t *testing.T) {
	for _, tc := range []struct {
		got, want string
	}{
		{count(0, "object"), "0 objects"},
		{count(1, "object"), "1 object"},
		{count(uint64(1), "encrypted object"), "1 encrypted object"},
		{count(2, "OS CA"), "2 OS CAs"},
	} {
		if tc.got != tc.want {
			t.Errorf("%q, want %q", tc.got, tc.want)
		}
	}
}
