package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/uki/ukitest"
	"github.com/trevex/chalkos/pkg/verity"
)

// upgradeManifest is a cluster of three control planes, three workers and a node without
// Kubernetes.
func upgradeManifest(controlPlanes int) string {
	node := func(name, role string, n int) string {
		return fmt.Sprintf(`%q: {"role": %q, "identity": {"hostname": %q, "network": {"networks": {"10-uplink": {"address": ["10.0.0.%d/24"]}}}, "networkUnits": {}, "labels": {}, "taints": [], "storage": {"disks": {}, "volumes": {}, "fallback": "none", "encryption": "none"}, "extensions": {}}}`, name, role, name, n)
	}
	var nodes []string
	for i := 1; i <= controlPlanes; i++ {
		nodes = append(nodes, node(fmt.Sprintf("cp%d", i), "cp", 10+i))
	}
	for i := 1; i <= 3; i++ {
		nodes = append(nodes, node(fmt.Sprintf("w%d", i), "w", 20+i))
	}
	nodes = append(nodes, node("n1", "plain", 31))
	return `{"schemaVersion": 0, "cluster": {"name": "lab", "endpoint": "https://10.0.0.10:6443"},
	  "roles": {"cp": {"image": "roles.cp.image", "kind": "controlplane"}, "w": {"image": "roles.w.image", "kind": "worker"}, "plain": {"image": "roles.plain.image"}},
	  "nodes": {` + strings.Join(nodes, ",\n") + `}}`
}

// testUpgradeImage builds a disk image of the role and version as chalkos images are laid out:
// an ESP holding the UKI, the store's hash tree and the store, described by repart-output.json.
func testUpgradeImage(t *testing.T, cluster, role, version string) string {
	t.Helper()
	for _, tool := range []string{"mkfs.vfat", "mmd", "mcopy", "mdir"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not in PATH", tool)
		}
	}
	dir := t.TempDir()
	store := bytes.Repeat([]byte(version+role), 300*512)[:300*512]
	tree, root, err := verity.Tree(bytes.NewReader(store), verity.Superblock{DataBlockSize: 512, HashBlockSize: 512, DataBlocks: 300, Salt: []byte(version)})
	if err != nil {
		t.Fatal(err)
	}
	esp := filepath.Join(dir, "esp.img")
	uki := filepath.Join(dir, "uki.efi")
	writeFile(t, uki, string(ukitest.UKI(map[string]string{
		"IMAGE_ID": "chalkos", "IMAGE_VERSION": version, "CHALKOS_CLUSTER": cluster, "CHALKOS_ROLE": role, "CHALKOS_BOOT_TRIES": "3",
	}, fmt.Sprintf("init=/x usrhash=%x", root))))
	for _, args := range [][]string{
		{"mkfs.vfat", "-C", esp, "4096"},
		{"mmd", "-i", esp, "::/EFI", "::/EFI/Linux"},
		{"mcopy", "-i", esp, uki, "::/EFI/Linux/chalkos_" + version + ".efi"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = append(os.Environ(), "MTOOLS_SKIP_CHECK=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
	}
	espData, err := os.ReadFile(esp)
	if err != nil {
		t.Fatal(err)
	}
	const espSize, treeSize, storeSize = 4 << 20, 64 << 10, 1 << 20
	raw := make([]byte, espSize+treeSize+storeSize)
	copy(raw, espData)
	copy(raw[espSize:], tree)
	copy(raw[espSize+treeSize:], store)
	imgDir := filepath.Join(dir, "image")
	writeFile(t, filepath.Join(imgDir, "chalkos_"+version+".raw"), string(raw))
	parts, _ := json.Marshal([]image.Partition{
		{Type: "esp", Label: "esp", Offset: 0, RawSize: espSize},
		{Type: "usr-x86-64-verity", Label: "store-verity_" + version, Offset: espSize, RawSize: treeSize, RootHash: hex.EncodeToString(root)},
		{Type: "usr-x86-64", Label: "store_" + version, Offset: espSize + treeSize, RawSize: storeSize, RootHash: hex.EncodeToString(root)},
	})
	writeFile(t, filepath.Join(imgDir, "repart-output.json"), string(parts))
	return imgDir
}

// upgradeFake answers chalkctl upgrade as chalkd does, keeping in memory what the node's disk and
// the cluster would: the version it runs, one installed beside it, one it fell back from, and
// whether its Node is cordoned.
type upgradeFake struct {
	nodev1connect.UnimplementedNodeServiceHandler
	lab        *upgradeLab
	name, role string
	// kind is the node's Kubernetes kind, "" without Kubernetes.
	kind string

	mu                      sync.Mutex
	version, staged, failed string
	// rebooting makes the node unreachable, as while it reboots.
	rebooting bool
}

// upgradeLab is a cluster of fake nodes.
type upgradeLab struct {
	t     *testing.T
	ta    *testApp
	nodes map[string]*upgradeFake
	addrs map[string]string

	mu sync.Mutex
	// events records what the nodes were asked, in order.
	events []string
	// unhealthy versions never become healthy, so their nodes fall back.
	unhealthy map[string]bool
	// unhealthyMembers are etcd members that do not answer.
	unhealthyMembers []string
	// cordoned holds the cordoned Nodes, and whether an upgrade marked them so.
	cordoned map[string]bool
	// refuseUpgrade makes the node refuse its next upgrade, as if the transfer broke.
	refuseUpgrade string
}

func (l *upgradeLab) record(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, fmt.Sprintf(format, args...))
}

func (l *upgradeLab) takeEvents() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	events := l.events
	l.events = nil
	return events
}

func (n *upgradeFake) down() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.rebooting {
		return connect.NewError(connect.CodeUnavailable, errors.New("the node reboots"))
	}
	return nil
}

func (n *upgradeFake) Info(context.Context, *connect.Request[nodev1.InfoRequest]) (*connect.Response[nodev1.InfoResponse], error) {
	if err := n.down(); err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return connect.NewResponse(&nodev1.InfoResponse{Version: n.version, ImageId: "chalkos", Cluster: "lab", Role: n.role}), nil
}

func (n *upgradeFake) Status(context.Context, *connect.Request[nodev1.StatusRequest]) (*connect.Response[nodev1.StatusResponse], error) {
	if err := n.down(); err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	st := &nodev1.StatusResponse{Boot: &nodev1.BootStatus{Version: n.version, Entry: "chalkos_" + n.version + ".efi", Blessed: true, Staged: n.staged, Failed: n.failed}}
	if n.failed != "" {
		st.Boot.Journal = []string{"chalkd: not healthy yet: units failed: broken.service"}
	}
	switch n.kind {
	case manifest.KindControlPlane:
		st.Kubernetes = &nodev1.KubernetesStatus{Kind: n.kind, State: "bootstrapped", NodeReady: "True"}
	case manifest.KindWorker:
		st.Kubernetes = &nodev1.KubernetesStatus{Kind: n.kind, State: "joined", NodeReady: "True"}
	}
	return connect.NewResponse(st), nil
}

func (n *upgradeFake) Upgrade(ctx context.Context, stream *connect.ClientStream[nodev1.UpgradeRequest]) (*connect.Response[nodev1.UpgradeResponse], error) {
	if err := n.down(); err != nil {
		return nil, err
	}
	if !stream.Receive() {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no header"))
	}
	h := stream.Msg().GetHeader()
	var received uint64
	for stream.Receive() {
		received += uint64(len(stream.Msg().GetChunk().GetData()))
	}
	n.lab.record("%s upgrade", n.name)
	n.lab.mu.Lock()
	refuse := n.lab.refuseUpgrade == n.name
	if refuse {
		n.lab.refuseUpgrade = ""
	}
	n.lab.mu.Unlock()
	if refuse {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("upgrade: receive the store: unexpected EOF"))
	}
	if want := h.StoreSize + h.VeritySize + h.UkiSize; received != want || h.Role != n.role {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("received %d bytes of %d for the role %s", received, want, h.Role))
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if h.Version == n.version {
		return connect.NewResponse(&nodev1.UpgradeResponse{AlreadyInstalled: true}), nil
	}
	n.staged, n.failed = h.Version, ""
	return connect.NewResponse(&nodev1.UpgradeResponse{Entry: "chalkos_" + h.Version + "+3.efi"}), nil
}

// Reboot boots the staged version, which falls back after its tries when the lab makes it
// unhealthy.
func (n *upgradeFake) Reboot(context.Context, *connect.Request[nodev1.RebootRequest]) (*connect.Response[nodev1.RebootResponse], error) {
	if err := n.down(); err != nil {
		return nil, err
	}
	n.lab.record("%s reboot", n.name)
	n.mu.Lock()
	n.rebooting = true
	n.mu.Unlock()
	go func() {
		time.Sleep(30 * time.Millisecond)
		n.lab.mu.Lock()
		unhealthy := n.lab.unhealthy[n.staged]
		n.lab.mu.Unlock()
		n.mu.Lock()
		defer n.mu.Unlock()
		n.rebooting = false
		switch {
		case n.staged == "":
		case unhealthy:
			n.failed, n.staged = n.staged, ""
		default:
			n.version, n.staged = n.staged, ""
		}
	}()
	return connect.NewResponse(&nodev1.RebootResponse{}), nil
}

func (n *upgradeFake) EtcdMembers(context.Context, *connect.Request[nodev1.EtcdMembersRequest]) (*connect.Response[nodev1.EtcdMembersResponse], error) {
	if err := n.down(); err != nil {
		return nil, err
	}
	if n.kind != manifest.KindControlPlane {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the node is a worker; ask a control-plane node"))
	}
	n.lab.record("%s members", n.name)
	resp := &nodev1.EtcdMembersResponse{}
	n.lab.mu.Lock()
	defer n.lab.mu.Unlock()
	for name, node := range n.lab.nodes {
		if node.kind == manifest.KindControlPlane {
			m := &nodev1.EtcdMember{Name: name}
			if slices.Contains(n.lab.unhealthyMembers, name) {
				m.Unhealthy = "no answer"
			}
			resp.Members = append(resp.Members, m)
		}
	}
	return connect.NewResponse(resp), nil
}

func (n *upgradeFake) DrainNode(_ context.Context, req *connect.Request[nodev1.DrainNodeRequest]) (*connect.Response[nodev1.DrainNodeResponse], error) {
	if err := n.down(); err != nil {
		return nil, err
	}
	n.lab.record("%s drain", req.Msg.Node)
	n.lab.mu.Lock()
	defer n.lab.mu.Unlock()
	marked, cordoned := n.lab.cordoned[req.Msg.Node]
	if !cordoned {
		n.lab.cordoned[req.Msg.Node], marked = true, true
	}
	return connect.NewResponse(&nodev1.DrainNodeResponse{Marked: marked, Evicted: []string{"default/web"}}), nil
}

func (n *upgradeFake) UncordonNode(_ context.Context, req *connect.Request[nodev1.UncordonNodeRequest]) (*connect.Response[nodev1.UncordonNodeResponse], error) {
	if err := n.down(); err != nil {
		return nil, err
	}
	n.lab.mu.Lock()
	defer n.lab.mu.Unlock()
	if !n.lab.cordoned[req.Msg.Node] {
		return connect.NewResponse(&nodev1.UncordonNodeResponse{}), nil
	}
	delete(n.lab.cordoned, req.Msg.Node)
	n.lab.events = append(n.lab.events, req.Msg.Node+" uncordon")
	return connect.NewResponse(&nodev1.UncordonNodeResponse{Uncordoned: true}), nil
}

func newUpgradeLab(t *testing.T, controlPlanes int) *upgradeLab {
	t.Helper()
	ta := newTestApp(t)
	ta.pollInterval = 5 * time.Millisecond
	m := upgradeManifest(controlPlanes)
	writeFile(t, filepath.Join(ta.dir, "manifest.json"), m)
	ta.manifest, _ = manifest.Decode(strings.NewReader(m))
	l := &upgradeLab{t: t, ta: ta, nodes: map[string]*upgradeFake{}, addrs: map[string]string{}, unhealthy: map[string]bool{}, cordoned: map[string]bool{}}
	for name, node := range ta.manifest.Nodes {
		n := &upgradeFake{lab: l, name: name, role: node.Role, kind: ta.manifest.Roles[node.Role].Kind, version: "0.1.0"}
		dir := filepath.Join(t.TempDir(), "chalkd")
		cert, err := pki.IssueNode(ta.secrets.NodeCA, pki.NodeNames{CommonName: name, DNSNames: []string{name}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, chalkd.NodeCertificateFile), cert.Certificate+cert.Key)
		writeFile(t, filepath.Join(dir, chalkd.CAFile), ta.secrets.OSCA.Certificate)
		nc, err := chalkd.LoadNodeCertificate(dir)
		if err != nil {
			t.Fatal(err)
		}
		_, h := nodev1connect.NewNodeServiceHandler(n)
		l.addrs[name] = serveTLS(t, http.Handler(h), chalkd.TLSConfig(nc.GetCertificate, nc.ClientCAs))
		l.nodes[name] = n
	}
	return l
}

// upgrade runs chalkctl upgrade with the lab's cluster.
func (l *upgradeLab) upgrade(img string, args ...string) error {
	args = append([]string{"upgrade", "--image", img}, args...)
	args = append(args, "--manifest", filepath.Join(l.ta.dir, "manifest.json"), "--flake", l.ta.dir)
	for name, addr := range l.addrs {
		args = append(args, "--endpoint", name+"="+addr)
	}
	return l.ta.run(context.Background(), args)
}

func (l *upgradeLab) versions() map[string]string {
	versions := map[string]string{}
	for name, n := range l.nodes {
		n.mu.Lock()
		versions[name] = n.version
		n.mu.Unlock()
	}
	return versions
}

// TestUpgradeControlPlanesInTurn upgrades the control planes one at a time: each only while the
// others keep etcd's quorum, the image installed before it is drained, and uncordoned once back.
func TestUpgradeControlPlanesInTurn(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.upgrade(testUpgradeImage(t, "lab", "cp", "0.2.0")); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	var want []string
	for _, cp := range []string{"cp1", "cp2", "cp3"} {
		want = append(want, cp+" members", cp+" upgrade", cp+" drain", cp+" reboot", cp+" uncordon")
	}
	if got := l.takeEvents(); !slices.Equal(got, want) {
		t.Errorf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for name, v := range l.versions() {
		if want := map[bool]string{true: "0.2.0", false: "0.1.0"}[strings.HasPrefix(name, "cp")]; v != want {
			t.Errorf("%s runs %s, want %s", name, v, want)
		}
	}
	if !strings.Contains(l.ta.stdout.String(), "upgraded 3 nodes to 0.2.0") {
		t.Errorf("output:\n%s", l.ta.stdout)
	}

	// Run again, it finds every control plane upgraded.
	if err := l.upgrade(testUpgradeImage(t, "lab", "cp", "0.2.0")); err != nil {
		t.Fatal(err)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("a second run asked %q", got)
	}
}

// TestUpgradeKeepsQuorum refuses a control plane whose reboot would leave etcd without its
// quorum, and a single control plane without --allow-downtime.
func TestUpgradeKeepsQuorum(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.unhealthyMembers = []string{"cp2"}
	err := l.upgrade(testUpgradeImage(t, "lab", "cp", "0.2.0"), "--nodes", "cp1", "--timeout", "100ms")
	if err == nil || !strings.Contains(err.Error(), "without cp1 etcd has 1 healthy voters of 3, fewer than the 2 its quorum needs") {
		t.Errorf("upgrade = %v", err)
	}
	for _, e := range l.takeEvents() {
		if !strings.HasSuffix(e, " members") {
			t.Errorf("the refused run asked %q", e)
		}
	}

	single := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "lab", "cp", "0.2.0")
	if err := single.upgrade(img); err == nil || !strings.Contains(err.Error(), "--allow-downtime") {
		t.Errorf("upgrade of a single control plane = %v", err)
	}
	if err := single.upgrade(img, "--allow-downtime"); err != nil {
		t.Fatal(err)
	}
	if v := single.versions()["cp1"]; v != "0.2.0" {
		t.Errorf("cp1 runs %s", v)
	}
}

// TestUpgradeWorkersInBatches drains each worker before it gets the image, takes at most
// --max-unavailable at once, and starts a batch once the one before is back.
func TestUpgradeWorkersInBatches(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.upgrade(testUpgradeImage(t, "lab", "w", "0.2.0"), "--max-unavailable", "2"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	events := l.takeEvents()
	at := func(e string) int {
		i := slices.Index(events, e)
		if i < 0 {
			t.Errorf("no %q in %q", e, events)
		}
		return i
	}
	for _, w := range []string{"w1", "w2", "w3"} {
		if !(at(w+" drain") < at(w+" upgrade") && at(w+" upgrade") < at(w+" reboot") && at(w+" reboot") < at(w+" uncordon")) {
			t.Errorf("%s out of order: %q", w, events)
		}
	}
	if at("w3 drain") < at("w1 uncordon") || at("w3 drain") < at("w2 uncordon") {
		t.Errorf("w3 started before the first batch was back: %q", events)
	}
	if at("w2 drain") > at("w1 uncordon") {
		t.Errorf("w1 and w2 were not upgraded together: %q", events)
	}
	for _, e := range events {
		if strings.HasPrefix(e, "cp") || strings.HasPrefix(e, "n1") {
			t.Errorf("a worker run touched %q", e)
		}
	}
}

// TestUpgradeStopsAtARollback stops at the first node that falls back, naming the version it runs
// again and what the failed boots logged; the node stays cordoned.
func TestUpgradeStopsAtARollback(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.unhealthy["0.3.0"] = true
	img := testUpgradeImage(t, "lab", "w", "0.3.0")
	err := l.upgrade(img)
	if err == nil || !strings.Contains(err.Error(), "w1: upgrade to 0.3.0 failed: rolled back to 0.1.0") || !strings.Contains(err.Error(), "units failed: broken.service") {
		t.Fatalf("upgrade = %v", err)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"w1 drain", "w1 upgrade", "w1 reboot"}) {
		t.Errorf("events %q", got)
	}
	if !l.cordoned["w1"] {
		t.Error("w1 was uncordoned")
	}
	// Run again, it stops at the same node before changing anything.
	if err := l.upgrade(img); err == nil || !strings.Contains(err.Error(), "rolled back to 0.1.0") {
		t.Errorf("a second run = %v", err)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("a second run asked %q", got)
	}
}

// TestUpgradeContinues runs again after a run stopped: a node that runs the image is skipped, a
// node with the image installed is rebooted without sending it again, and only nodes the
// upgrade cordoned are uncordoned.
func TestUpgradeContinues(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "lab", "w", "0.2.0")
	l.refuseUpgrade = "w2"
	if err := l.upgrade(img); err == nil || !strings.HasPrefix(err.Error(), "w2: ") || !strings.Contains(err.Error(), "upgrade: receive the store") {
		t.Fatalf("upgrade = %v", err)
	}
	l.takeEvents()
	l.nodes["w3"].staged = "0.2.0"
	l.cordoned["w3"] = false
	if err := l.upgrade(img); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	want := []string{"w2 drain", "w2 upgrade", "w2 reboot", "w2 uncordon", "w3 drain", "w3 reboot"}
	if got := l.takeEvents(); !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	if marked, cordoned := l.cordoned["w3"]; !cordoned || marked {
		t.Error("the upgrade uncordoned w3, which an operator had cordoned")
	}
	for _, w := range []string{"w1", "w2", "w3"} {
		if v := l.versions()[w]; v != "0.2.0" {
			t.Errorf("%s runs %s", w, v)
		}
	}
}

// TestUpgradeWithoutReboot installs the image on every node of the role and leaves them as they
// run.
func TestUpgradeWithoutReboot(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.upgrade(testUpgradeImage(t, "lab", "cp", "0.2.0"), "--no-reboot"); err != nil {
		t.Fatal(err)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"cp1 upgrade", "cp2 upgrade", "cp3 upgrade"}) {
		t.Errorf("events %q", got)
	}
	if n := l.nodes["cp2"]; n.staged != "0.2.0" || n.version != "0.1.0" {
		t.Errorf("cp2 runs %s with %s staged", n.version, n.staged)
	}
}

// TestUpgradeWithAnOperatorClientFile runs an upgrade without the secrets file.
func TestUpgradeWithAnOperatorClientFile(t *testing.T) {
	l := newUpgradeLab(t, 3)
	config := l.ta.writeConfig(t, "ops", pki.RoleOperator, time.Hour, time.Now())
	l.ta.withoutSecrets(t)
	if err := l.upgrade(testUpgradeImage(t, "lab", "plain", "0.2.0"), "--config", config); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if v := l.versions()["n1"]; v != "0.2.0" {
		t.Errorf("n1 runs %s", v)
	}
}

func testSigner(t *testing.T, dir, name string) (key, cert string) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	key, cert = filepath.Join(dir, name+".key"), filepath.Join(dir, name+".crt")
	writeFile(t, key, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})))
	writeFile(t, cert, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
	return key, cert
}

// TestUpgradeChecksTheImage refuses images of another cluster or role, and images whose UKI the
// cluster's db certificate did not sign, before any node is asked to change.
func TestUpgradeChecksTheImage(t *testing.T) {
	if _, err := exec.LookPath("sbsign"); err != nil {
		t.Skip("sbsign not in PATH")
	}
	l := newUpgradeLab(t, 3)
	dir := t.TempDir()
	dbKey, dbCert := testSigner(t, dir, "db")
	otherKey, otherCert := testSigner(t, dir, "other")
	certPEM, _ := os.ReadFile(dbCert)
	l.ta.editManifest(t, func(m *manifest.Manifest) { m.SecureBoot.SignerCertificate = string(certPEM) })
	img := testUpgradeImage(t, "lab", "w", "0.2.0")
	for _, tc := range []struct {
		name string
		img  string
		args []string
		want string
	}{
		{"another cluster", testUpgradeImage(t, "prod", "w", "0.2.0"), nil, "the image is of the cluster prod, not lab"},
		{"a node of another role", img, []string{"--nodes", "cp1", "--sign-key", dbKey, "--sign-cert", dbCert}, "cp1 is a node of the role cp; the image is of w"},
		{"an unsigned UKI", img, nil, "Secure Boot would refuse the image's UKI: the image is not signed"},
		{"a UKI signed by another key", img, []string{"--sign-key", otherKey, "--sign-cert", otherCert}, "not in db"},
		{"a version with upper-case letters", testUpgradeImage(t, "lab", "w", "0.2.0-RC1"), []string{"--sign-key", dbKey, "--sign-cert", dbCert}, "is not one an upgrade installs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := l.upgrade(tc.img, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("upgrade = %v, want %q", err, tc.want)
			}
			if got := l.takeEvents(); len(got) != 0 {
				t.Errorf("asked %q", got)
			}
		})
	}
	if err := l.upgrade(img, "--sign-key", dbKey, "--sign-cert", dbCert); err != nil {
		t.Fatal(err)
	}
}
