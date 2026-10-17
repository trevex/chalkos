package chalkctl

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
	"maps"
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
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/uki/ukitest"
	"github.com/trevex/chalkos/pkg/verity"
)

// testUpgradeImage builds a metal disk image of the variant as chalkos images are laid out: an
// ESP holding systemd-boot and the UKI, the store's hash tree and the store, described by
// repart-output.json. The name seeds the store, so images of different names or variants differ
// in their root hashes, and the fake nodes say which image they run by it.
func testUpgradeImage(t *testing.T, variant, name string) string {
	t.Helper()
	return testUpgradeImageOfBlocks(t, variant, "metal", name, 512)
}

// testImageNames names the test images by their root hashes.
var testImageNames sync.Map

// rootOf is the root hash of a test image's store, as its directory's metadata.json names it.
func rootOf(t *testing.T, dir string) []byte {
	t.Helper()
	md, err := schema.ReadMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	root, err := hex.DecodeString(md.RootHash)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// shortOf is the name chalkctl gives a test image, which names no revision or tag.
func shortOf(t *testing.T, dir string) string {
	t.Helper()
	return imageName(rootOf(t, dir), "", "")
}

// testUpgradeImageOfBlocks builds a test image of the platform whose store has dm-verity blocks of
// the size.
func testUpgradeImageOfBlocks(t *testing.T, variant, platform, name string, blockSize int) string {
	t.Helper()
	return testImageOf(t, variant, platform, name, blockSize, nil)
}

// testImageOf builds a test image whose UKI's os-release edit changes, when given, from what
// chalkos's images name, with an empty IMAGE_VERSION.
func testImageOf(t *testing.T, variant, platform, name string, blockSize int, edit func(osRelease map[string]string)) string {
	t.Helper()
	for _, tool := range []string{"mkfs.vfat", "mmd", "mcopy", "mdir"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not in PATH", tool)
		}
	}
	dir := t.TempDir()
	store := bytes.Repeat([]byte(name+variant+platform), 300*blockSize)[:300*blockSize]
	tree, root, err := verity.Tree(bytes.NewReader(store), verity.Superblock{DataBlockSize: uint32(blockSize), HashBlockSize: uint32(blockSize), DataBlocks: 300, Salt: []byte(name)})
	if err != nil {
		t.Fatal(err)
	}
	testImageNames.Store(string(root), name)
	esp := filepath.Join(dir, "esp.img")
	uki := filepath.Join(dir, "uki.efi")
	loader := filepath.Join(dir, "systemd-boot.efi")
	writeFile(t, loader, string(ukitest.Build(map[string][]byte{".text": []byte("systemd-boot")})))
	osRelease := map[string]string{
		"IMAGE_ID": "chalkos", "IMAGE_VERSION": "", "CHALKOS_VARIANT": variant, "CHALKOS_PLATFORM": platform, "CHALKOS_BOOT_TRIES": "3",
	}
	if edit != nil {
		edit(osRelease)
	}
	writeFile(t, uki, string(ukitest.UKI(osRelease, fmt.Sprintf("init=/x usrhash=%x", root))))
	for _, args := range [][]string{
		{"mkfs.vfat", "-C", esp, "4096"},
		{"mmd", "-i", esp, "::/EFI", "::/EFI/BOOT", "::/EFI/Linux"},
		{"mcopy", "-i", esp, uki, "::/EFI/Linux/chalkos_1.efi"},
		{"mcopy", "-i", esp, loader, "::/EFI/BOOT/BOOTX64.EFI"},
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
	const espSize, treeSize, storeSize = 4 << 20, 64 << 10, 2 << 20
	raw := make([]byte, espSize+treeSize+storeSize)
	copy(raw, espData)
	copy(raw[espSize:], tree)
	copy(raw[espSize+treeSize:], store)
	imgDir := filepath.Join(dir, "image")
	writeFile(t, filepath.Join(imgDir, "chalkos.raw"), string(raw))
	id := hex.EncodeToString(root)[:16]
	parts, _ := json.Marshal([]image.Partition{
		{Type: "esp", Label: "esp", Offset: 0, RawSize: espSize},
		{Type: "usr-x86-64-verity", Label: "store-verity_" + id, Offset: espSize, RawSize: treeSize, RootHash: hex.EncodeToString(root)},
		{Type: "usr-x86-64", Label: "store_" + id, Offset: espSize + treeSize, RawSize: storeSize, RootHash: hex.EncodeToString(root)},
	})
	writeFile(t, filepath.Join(imgDir, "repart-output.json"), string(parts))
	// The contract chalkctl checks a node's config against, and the metadata it reads the image's
	// root hash from: the upgrade lab's variants cp, w and k8s run Kubernetes.
	copySchemas(t, filepath.Join(imgDir, "schemas"))
	k := "null"
	if variant == "cp" || variant == "w" || variant == "k8s" {
		writeFile(t, filepath.Join(imgDir, "image.json"), testImageJSON)
		k = `{"version": "1.37.1", "cni": "none"}`
	}
	writeFile(t, filepath.Join(imgDir, "metadata.json"), fmt.Sprintf(`{"schemaVersion": 0, "id": "chalkos", "variant": %q, "platform": %q, "rootHash": %q, "kubernetes": %s, "kinds": []}`,
		variant, platform, hex.EncodeToString(root), k))
	return imgDir
}

// upgradeFake answers chalkctl upgrade as chalkd does, keeping in memory what the node's disk and
// the cluster would: the image it runs, one installed beside it, one it fell back from, each by
// its test name and root hash, and whether its Node is cordoned.
type upgradeFake struct {
	nodev1connect.UnimplementedNodeServiceHandler
	lab                     *upgradeLab
	name, variant, platform string
	// kind is the node's Kubernetes kind, "" without Kubernetes.
	kind string

	mu                    sync.Mutex
	image, staged, failed string
	// root, stagedRoot and failedRoot are the root hashes of the stores of image, staged and
	// failed, and generation the generation of the entry the node booted image from.
	root, stagedRoot, failedRoot []byte
	generation                   uint64
	// config and stagedConfig are the config the node runs and the one kept for the staged image, as
	// delivered.
	config, stagedConfig string
	// secret is the fallback secret the last upgrade sent.
	secret string
	// stagedStoreSize and stagedVeritySize are the bytes of store and hash tree staged.
	stagedStoreSize, stagedVeritySize uint64
	// rebooting makes the node unreachable, as while it reboots.
	rebooting bool
	// alreadyStaged makes the node answer an upgrade as one that found the image staged already,
	// though its status did not say so, as when another run staged it in between.
	alreadyStaged bool
}

// upgradeLab is a cluster of fake nodes.
type upgradeLab struct {
	t     *testing.T
	ta    *testApp
	nodes map[string]*upgradeFake
	addrs map[string]string
	// inventory is the lab's inventory once useInventory wrote it.
	inventory string

	mu sync.Mutex
	// events records what the nodes were asked, in order.
	events []string
	// unhealthy images never become healthy, so their nodes fall back.
	unhealthy map[string]bool
	// unhealthyMembers are etcd members that do not answer.
	unhealthyMembers []string
	// cordoned holds the cordoned Nodes, and whether an upgrade marked them so.
	cordoned map[string]bool
	// refuseUpgrade makes the node refuse its next upgrade, as if the transfer broke.
	refuseUpgrade string
	// emptyDir holds the nodes drained deleting emptyDir data.
	emptyDir map[string]bool
	// notReady holds the nodes whose Node is not Ready.
	notReady map[string]bool
	// stopping holds the nodes whose drains time out while their pods stop.
	stopping map[string]bool
	// configDenied makes the nodes refuse GetConfig, as for a role below admin.
	configDenied bool
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
	return connect.NewResponse(&nodev1.InfoResponse{RootHash: n.root, ImageId: "chalkos", Variant: n.variant, Platform: n.platform}), nil
}

func (n *upgradeFake) Status(context.Context, *connect.Request[nodev1.StatusRequest]) (*connect.Response[nodev1.StatusResponse], error) {
	if err := n.down(); err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	st := &nodev1.StatusResponse{Boot: &nodev1.BootStatus{
		Running: &nodev1.BootImage{RootHash: n.root, Generation: n.generation},
		Entry:   fmt.Sprintf("chalkos_%d.efi", n.generation), Blessed: true,
	}}
	// VAR, which every node has.
	st.Volumes = []*nodev1.VolumeStatus{{Name: "var"}}
	if n.staged != "" {
		st.Boot.Staged = &nodev1.BootImage{RootHash: n.stagedRoot, Generation: n.generation + 1}
		st.StagedConfigVersion = versionOf(n.stagedConfig)
	}
	if n.failed != "" {
		st.Boot.Failed = &nodev1.BootImage{RootHash: n.failedRoot, Generation: n.generation + 1}
	}
	st.ConfigVersion = versionOf(n.config)
	n.lab.mu.Lock()
	ready := "True"
	if n.lab.notReady[n.name] {
		ready = "False"
	}
	n.lab.mu.Unlock()
	if n.failed != "" {
		st.Boot.Journal = []string{"chalkd: not healthy yet: units failed: broken.service"}
	}
	switch n.kind {
	case identity.KindControlPlane:
		st.Kubernetes = &nodev1.KubernetesStatus{Kind: n.kind, State: "bootstrapped", NodeReady: ready}
	case identity.KindWorker:
		st.Kubernetes = &nodev1.KubernetesStatus{Kind: n.kind, State: "joined", NodeReady: ready}
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
	header := stream.Msg().GetHeader()
	if header.GetConfig() != "" || header.GetFallbackSecret() != "" {
		if err := needsAdmin(ctx, "Upgrade with config or a fallback secret"); err != nil {
			return nil, err
		}
	}
	h := header.GetImage()
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
	if h.BootLoader != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("an upgrade leaves the boot loader as it is"))
	}
	if want := h.Store.Size + h.HashTree.Size + h.Uki.Size; received != want || h.Platform != n.platform {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("received %d bytes of %d for %s", received, want, h.Platform))
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if bytes.Equal(h.RootHash, n.root) {
		return connect.NewResponse(&nodev1.UpgradeResponse{AlreadyInstalled: true}), nil
	}
	name, _ := testImageNames.Load(string(h.RootHash))
	n.staged, n.stagedRoot, n.failed, n.failedRoot = name.(string), h.RootHash, "", nil
	n.stagedConfig, n.secret = header.GetConfig(), header.GetFallbackSecret()
	n.stagedStoreSize, n.stagedVeritySize = h.Store.Size, h.HashTree.Size
	if n.alreadyStaged {
		return connect.NewResponse(&nodev1.UpgradeResponse{AlreadyStaged: true, Entry: fmt.Sprintf("chalkos_%d+3.efi", n.generation+1)}), nil
	}
	return connect.NewResponse(&nodev1.UpgradeResponse{Entry: fmt.Sprintf("chalkos_%d+3.efi", n.generation+1)}), nil
}

// Reboot boots the staged image, which falls back after its tries when the lab makes it
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
			n.failed, n.failedRoot, n.staged, n.stagedRoot = n.staged, n.stagedRoot, "", nil
		default:
			n.image, n.root, n.staged, n.stagedRoot = n.staged, n.stagedRoot, "", nil
			n.config, n.stagedConfig = n.stagedConfig, ""
			n.generation++
		}
	}()
	return connect.NewResponse(&nodev1.RebootResponse{}), nil
}

func (n *upgradeFake) EtcdMembers(context.Context, *connect.Request[nodev1.EtcdMembersRequest]) (*connect.Response[nodev1.EtcdMembersResponse], error) {
	if err := n.down(); err != nil {
		return nil, err
	}
	if n.kind != identity.KindControlPlane {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the node is a worker; ask a control-plane node"))
	}
	n.lab.record("%s members", n.name)
	resp := &nodev1.EtcdMembersResponse{}
	n.lab.mu.Lock()
	defer n.lab.mu.Unlock()
	for name, node := range n.lab.nodes {
		if node.kind == identity.KindControlPlane {
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
	if n.lab.stopping[req.Msg.Node] {
		n.lab.cordoned[req.Msg.Node] = true
		return nil, connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf("drain %s: pods default/web still run: they did not stop; it stays cordoned", req.Msg.Node))
	}
	if req.Msg.DeleteEmptydirData {
		n.lab.emptyDir[req.Msg.Node] = true
	}
	marked, cordoned := n.lab.cordoned[req.Msg.Node]
	if !cordoned {
		n.lab.cordoned[req.Msg.Node], marked = true, true
	}
	return connect.NewResponse(&nodev1.DrainNodeResponse{Marked: marked, Evicted: []string{"default/web"}, Unmanaged: []string{"default/bare"}}), nil
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

// fakeRoleKey holds the role a fake node's client certificate grants.
type fakeRoleKey struct{}

// withRoles serves h with the role of each request's client certificate in its context, as chalkd
// derives it from the certificate's Organization.
func withRoles(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := ""
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			role, _ = pki.Role(r.TLS.PeerCertificates[0])
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), fakeRoleKey{}, role)))
	})
}

// needsAdmin refuses what only an admin may do, as chalkd does.
func needsAdmin(ctx context.Context, what string) error {
	if role, _ := ctx.Value(fakeRoleKey{}).(string); !pki.Allows(role, pki.RoleAdmin) {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s needs the admin role", what))
	}
	return nil
}
func newUpgradeLab(t *testing.T, controlPlanes int) *upgradeLab {
	t.Helper()
	return newUpgradeLabOf(t, controlPlanes, []string{"w1", "w2", "w3"}, "cp", "w")
}

// newUpgradeLabOf runs a fake chalkd for each of the control planes cp1 and on, of the variant
// cpVariant, the workers named, of the variant wVariant, which may be the same, and n1, a node of
// the variant plain without Kubernetes, and describes them in an inventory.
func newUpgradeLabOf(t *testing.T, controlPlanes int, workers []string, cpVariant, wVariant string) *upgradeLab {
	t.Helper()
	ta := newTestApp(t)
	ta.pollInterval = 5 * time.Millisecond
	var nodes []testNode
	for i := 1; i <= controlPlanes; i++ {
		nodes = append(nodes, testNode{fmt.Sprintf("cp%d", i), identity.KindControlPlane, cpVariant})
	}
	for _, w := range workers {
		nodes = append(nodes, testNode{w, identity.KindWorker, wVariant})
	}
	nodes = append(nodes, testNode{"n1", "", "plain"})
	l := &upgradeLab{t: t, ta: ta, nodes: map[string]*upgradeFake{}, addrs: map[string]string{}, unhealthy: map[string]bool{}, cordoned: map[string]bool{}, emptyDir: map[string]bool{}, stopping: map[string]bool{}, notReady: map[string]bool{}}
	for _, node := range nodes {
		name := node.name
		n := &upgradeFake{lab: l, name: name, variant: node.variant, platform: "metal", kind: node.kind, image: "0.1.0", root: bytes.Repeat([]byte{1}, 32), generation: 1}
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
		l.addrs[name] = serveTLS(t, withRoles(h), chalkd.TLSConfig(nc.GetCertificate, nc.ClientCAs))
		l.nodes[name] = n
	}
	l.useInventory(t, nil)
	return l
}

// upgrade runs chalkctl upgrade with the image and the lab's inventory.
func (l *upgradeLab) upgrade(img string, args ...string) error {
	return l.run(append([]string{"upgrade", "--image", img}, args...)...)
}

// useInventory describes the lab's nodes in an inventory, which the lab's runs read from then on:
// each node runs the Image of its variant on metal, whose directory images gives by variant.
func (l *upgradeLab) useInventory(t *testing.T, images map[string]string) {
	t.Helper()
	var nodes []testNode
	for _, name := range slices.Sorted(maps.Keys(l.nodes)) {
		n := l.nodes[name]
		nodes = append(nodes, testNode{name, n.kind, n.variant})
	}
	l.inventory = writeNodes(t, l.ta.dir, nodes, images)
}

// run runs chalkctl with the lab's inventory and every node's address.
func (l *upgradeLab) run(args ...string) error {
	args = append(args, "--inventory", l.inventory)
	for name, addr := range l.addrs {
		args = append(args, "--address", name+"="+addr)
	}
	return l.ta.run(context.Background(), args)
}

// TestUpgradeGivesEachNodeItsImage upgrades the nodes of an inventory without --image, each to the
// image its Image names, one image per directory, the control planes first; a node that runs
// another platform than its Image's stops the run.
func TestUpgradeGivesEachNodeItsImage(t *testing.T) {
	l := newUpgradeLab(t, 3)
	images := map[string]string{
		"cp": testUpgradeImage(t, "cp", "0.2.0"), "w": testUpgradeImage(t, "w", "0.2.0"), "plain": testUpgradeImage(t, "plain", "0.2.0"),
	}
	l.useInventory(t, images)
	if err := l.run("upgrade", "--max-unavailable", "4"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	for name, v := range l.images() {
		if v != "0.2.0" {
			t.Errorf("%s runs %s", name, v)
		}
	}
	events := l.takeEvents()
	if i := slices.Index(events, "cp3 uncordon"); i < 0 || slices.ContainsFunc(events[:i], func(e string) bool { return !strings.HasPrefix(e, "cp") }) {
		t.Errorf("events %q: the other nodes did not wait for the control planes", events)
	}
	cp, w, plain := shortOf(t, images["cp"]), shortOf(t, images["w"]), shortOf(t, images["plain"])
	for _, line := range []string{"upgrading w on metal to " + w + ": w1, w2, w3", "upgrading cp on metal to " + cp + ": cp1, cp2, cp3",
		"upgraded 7 nodes to " + strings.Join(slices.Sorted(slices.Values([]string{cp, w, plain})), ", ")} {
		if !strings.Contains(l.ta.stdout.String(), line) {
			t.Errorf("output lacks %q:\n%s", line, l.ta.stdout)
		}
	}
	l.nodes["w3"].platform = "kvm"
	if err := l.run("upgrade", "--nodes", "w3"); err == nil || !strings.Contains(err.Error(), "w3 runs an image built for kvm, but its Image w-metal is built for metal") {
		t.Errorf("a node on another platform = %v", err)
	}
}

// images names the image each node runs.
func (l *upgradeLab) images() map[string]string {
	images := map[string]string{}
	for name, n := range l.nodes {
		n.mu.Lock()
		images[name] = n.image
		n.mu.Unlock()
	}
	return images
}

// TestUpgradeControlPlanesInTurn upgrades the control planes one at a time: each only while the
// others keep etcd's quorum, the image installed before it is drained, and uncordoned once back.
func TestUpgradeControlPlanesInTurn(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "cp", "0.2.0")
	if err := l.upgrade(img); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	var want []string
	for _, cp := range []string{"cp1", "cp2", "cp3"} {
		want = append(want, cp+" members", cp+" upgrade", cp+" drain", cp+" reboot", cp+" uncordon")
	}
	if got := l.takeEvents(); !slices.Equal(got, want) {
		t.Errorf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for name, v := range l.images() {
		if want := map[bool]string{true: "0.2.0", false: "0.1.0"}[strings.HasPrefix(name, "cp")]; v != want {
			t.Errorf("%s runs %s, want %s", name, v, want)
		}
	}
	if !strings.Contains(l.ta.stdout.String(), "upgraded 3 nodes to "+shortOf(t, img)) {
		t.Errorf("output:\n%s", l.ta.stdout)
	}

	// Run again, it finds every control plane upgraded.
	if err := l.upgrade(img); err != nil {
		t.Fatal(err)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("a second run asked %q", got)
	}
}

// TestUpgradeOneVariantControlPlanesFirst upgrades control planes and workers that share a variant and
// its image: the control planes one at a time, each back before the next, and the workers after
// them all, although their names sort first, each drained before it gets the image and without a
// check of etcd's quorum.
func TestUpgradeOneVariantControlPlanesFirst(t *testing.T) {
	l := newUpgradeLabOf(t, 3, []string{"a1", "a2"}, "k8s", "k8s")
	if err := l.upgrade(testUpgradeImage(t, "k8s", "0.2.0")); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	events := l.takeEvents()
	at := func(e string) int {
		i := slices.Index(events, e)
		if i < 0 {
			t.Fatalf("no %q in %q", e, events)
		}
		return i
	}
	cps := []string{"cp1", "cp2", "cp3"}
	for i, cp := range cps {
		if !(at(cp+" upgrade") < at(cp+" drain") && at(cp+" drain") < at(cp+" reboot") && at(cp+" reboot") < at(cp+" uncordon")) {
			t.Errorf("%s out of order: %q", cp, events)
		}
		if i > 0 && at(cps[i-1]+" uncordon") > at(cp+" upgrade") {
			t.Errorf("%s started before %s was back: %q", cp, cps[i-1], events)
		}
	}
	for _, w := range []string{"a1", "a2"} {
		if at(w+" drain") < at("cp3 uncordon") {
			t.Errorf("%s was drained before the control planes were upgraded: %q", w, events)
		}
		if !(at(w+" drain") < at(w+" upgrade") && at(w+" upgrade") < at(w+" reboot") && at(w+" reboot") < at(w+" uncordon")) {
			t.Errorf("%s out of order: %q", w, events)
		}
	}
	// etcd's quorum is checked before each control plane reboots, and for no worker.
	var members []int
	for i, e := range events {
		if strings.HasSuffix(e, " members") {
			members = append(members, i)
		}
	}
	if len(members) != 3 || members[2] > at("a1 drain") || members[2] > at("a2 drain") {
		t.Errorf("etcd's members were asked at %v, want three times before any worker is drained: %q", members, events)
	}
	for name, v := range l.images() {
		if want := map[bool]string{true: "0.1.0", false: "0.2.0"}[name == "n1"]; v != want {
			t.Errorf("%s runs %s, want %s", name, v, want)
		}
	}
}

// TestUpgradeKeepsQuorum refuses a control plane whose reboot would leave etcd without its
// quorum, and one or two control planes, which lose it while one reboots, without
// --allow-downtime.
func TestUpgradeKeepsQuorum(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.unhealthyMembers = []string{"cp2"}
	err := l.upgrade(testUpgradeImage(t, "cp", "0.2.0"), "--nodes", "cp1", "--timeout", "100ms")
	if err == nil || !strings.Contains(err.Error(), "without cp1 etcd has 1 healthy voter of 3, fewer than the 2 its quorum needs") {
		t.Errorf("upgrade = %v", err)
	}
	for _, e := range l.takeEvents() {
		if !strings.HasSuffix(e, " members") {
			t.Errorf("the refused run asked %q", e)
		}
	}

	single := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "cp", "0.2.0")
	if err := single.upgrade(img); err == nil || !strings.Contains(err.Error(), "etcd has 1 voter, and without cp1") || !strings.Contains(err.Error(), "--allow-downtime") {
		t.Errorf("upgrade of a single control plane = %v", err)
	}
	if err := single.upgrade(img, "--allow-downtime"); err != nil {
		t.Fatal(err)
	}
	if v := single.images()["cp1"]; v != "0.2.0" {
		t.Errorf("cp1 runs %s", v)
	}
	if !strings.Contains(single.ta.stdout.String(), "upgraded 1 node to "+shortOf(t, img)) {
		t.Errorf("output:\n%s", single.ta.stdout)
	}

	// Two control planes lose their quorum while either reboots.
	two := newUpgradeLab(t, 2)
	if err := two.upgrade(img); err == nil || !strings.Contains(err.Error(), "--allow-downtime") {
		t.Errorf("upgrade of two control planes = %v", err)
	}
	if err := two.upgrade(img, "--allow-downtime"); err != nil {
		t.Fatal(err)
	}
	if v := two.images(); v["cp1"] != "0.2.0" || v["cp2"] != "0.2.0" {
		t.Errorf("the control planes run %v", v)
	}
}

// TestUpgradeWorkersInBatches drains each worker before it gets the image, takes at most
// --max-unavailable at once, and starts a batch once the one before is back.
func TestUpgradeWorkersInBatches(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.upgrade(testUpgradeImage(t, "w", "0.2.0"), "--max-unavailable", "2"); err != nil {
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

// TestUpgradeStopsAtARollback stops at the first node that falls back, naming the image it runs
// again and what the failed boots logged; the node stays cordoned.
func TestUpgradeStopsAtARollback(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.unhealthy["0.3.0"] = true
	img := testUpgradeImage(t, "w", "0.3.0")
	err := l.upgrade(img)
	if err == nil || !strings.Contains(err.Error(), "w1: upgrade to "+shortOf(t, img)+" failed: rolled back to 010101010101; it stays cordoned") || !strings.Contains(err.Error(), "units failed: broken.service") {
		t.Fatalf("upgrade = %v", err)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"w1 drain", "w1 upgrade", "w1 reboot"}) {
		t.Errorf("events %q", got)
	}
	if !l.cordoned["w1"] {
		t.Error("w1 was uncordoned")
	}
	// Run again, it stops at the same node before changing anything.
	if err := l.upgrade(img); err == nil || !strings.Contains(err.Error(), "rolled back to 010101010101") {
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
	img := testUpgradeImage(t, "w", "0.2.0")
	l.refuseUpgrade = "w2"
	if err := l.upgrade(img); err == nil || !strings.HasPrefix(err.Error(), "w2: ") || !strings.Contains(err.Error(), "upgrade: receive the store") {
		t.Fatalf("upgrade = %v", err)
	}
	l.takeEvents()
	l.nodes["w3"].staged, l.nodes["w3"].stagedRoot = "0.2.0", l.nodes["w1"].root
	l.nodes["w3"].stagedConfig = l.inventoryConfig("w3")
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
		if v := l.images()[w]; v != "0.2.0" {
			t.Errorf("%s runs %s", w, v)
		}
	}
}

// TestUpgradeSendsOverAnotherStagedImage sends the image to a node that has another image staged,
// whatever its build was named.
func TestUpgradeSendsOverAnotherStagedImage(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "w", "0.2.0")
	l.nodes["w3"].staged, l.nodes["w3"].stagedRoot = "0.2.0", bytes.Repeat([]byte{9}, 32)
	if err := l.upgrade(img); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got := l.takeEvents(); !slices.Contains(got, "w3 upgrade") {
		t.Errorf("events %q, want the image sent to w3", got)
	}
	if n := l.nodes["w3"]; n.image != "0.2.0" || !bytes.Equal(n.root, rootOf(t, img)) {
		t.Errorf("w3 runs %s of the root hash %x", n.image, n.root)
	}
}

// TestUpgradeWithoutReboot installs the image on every node of the variant and leaves them as they
// run.
func TestUpgradeWithoutReboot(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.upgrade(testUpgradeImage(t, "cp", "0.2.0"), "--no-reboot"); err != nil {
		t.Fatal(err)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"cp1 upgrade", "cp2 upgrade", "cp3 upgrade"}) {
		t.Errorf("events %q", got)
	}
	if n := l.nodes["cp2"]; n.staged != "0.2.0" || n.image != "0.1.0" {
		t.Errorf("cp2 runs %s with %s staged", n.image, n.staged)
	}
}

// TestUpgradeReportsAStagedImage says so when a node answers that the image is staged already,
// which its status did not say, and goes on as after an install.
func TestUpgradeReportsAStagedImage(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "w", "0.2.0")
	l.nodes["w1"].alreadyStaged = true
	if err := l.upgrade(img, "--nodes", "w1", "--no-reboot"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if want := "w1: " + shortOf(t, img) + " is staged already, as chalkos_2+3.efi\n"; !strings.Contains(l.ta.stdout.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, l.ta.stdout)
	}
	if n := l.nodes["w1"]; n.staged != "0.2.0" || n.image != "0.1.0" {
		t.Errorf("w1 runs %s with %s staged", n.image, n.staged)
	}
}

// TestUpgradeWith4KiBVerityBlocks sends the store of an image built with 4 KiB dm-verity blocks
// and its hash tree, as long as the tree's superblock counts them.
func TestUpgradeWith4KiBVerityBlocks(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.upgrade(testUpgradeImageOfBlocks(t, "cp", "metal", "0.2.0", 4096), "--no-reboot"); err != nil {
		t.Fatal(err)
	}
	// 300 data blocks; the superblock's block, three of digests and their root.
	if n := l.nodes["cp1"]; n.staged != "0.2.0" || n.stagedStoreSize != 300*4096 || n.stagedVeritySize != 5*4096 {
		t.Errorf("cp1 staged %s with %d bytes of store and %d of hash tree", n.staged, n.stagedStoreSize, n.stagedVeritySize)
	}
}

// TestUpgradeWithAnOperatorClientFile runs an upgrade without the secrets file. The operator role
// cannot deliver config, so chalkctl sends none, says so once, and each node carries its own over;
// --diff, which compares the config an upgrade delivers, needs the admin role.
func TestUpgradeWithAnOperatorClientFile(t *testing.T) {
	l := newUpgradeLab(t, 3)
	config := l.ta.writeConfig(t, "ops", pki.RoleOperator, time.Hour, time.Now())
	l.ta.withoutSecrets(t)
	l.nodes["n1"].config = "{}"
	if err := l.upgrade(testUpgradeImage(t, "plain", "0.2.0"), "--config", config, "--no-reboot"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if n := l.nodes["n1"]; n.staged != "0.2.0" || n.stagedConfig != "" {
		t.Errorf("n1 stages %q with the config %q, want 0.2.0 without config", n.staged, n.stagedConfig)
	}
	out := l.ta.stdout.String()
	if line := "config not delivered: the operator role cannot deliver config; each node carries its own over\n"; strings.Count(out, line) != 1 || strings.Contains(out, "n1: config") {
		t.Errorf("output:\n%s\nwant %q once and no node's config line", out, line)
	}
	err := l.upgrade(testUpgradeImage(t, "plain", "0.2.0"), "--config", config, "--diff")
	if err == nil || !strings.Contains(err.Error(), "upgrade --diff compares the config an upgrade delivers, which needs the admin role") {
		t.Errorf("upgrade --diff as an operator = %v", err)
	}
}

// TestUpgradeWithholdsConfigANodeDoesNotShow sends no config to nodes that refuse to show theirs
// to the credentials, as they would refuse config with the upgrade, and says so once; --diff stops
// there instead.
func TestUpgradeWithholdsConfigANodeDoesNotShow(t *testing.T) {
	l := newUpgradeLab(t, 1)
	l.configDenied = true
	img := testUpgradeImage(t, "w", "0.2.0")
	err := l.upgrade(img, "--diff")
	if err == nil || !strings.Contains(err.Error(), "needs the admin role") {
		t.Errorf("upgrade --diff = %v, want the admin role named", err)
	}
	if err := l.upgrade(img, "--nodes", "w1,w2", "--no-reboot", "--max-unavailable", "2"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	for _, name := range []string{"w1", "w2"} {
		if n := l.nodes[name]; n.staged != "0.2.0" || n.stagedConfig != "" {
			t.Errorf("%s stages %q with the config %q, want 0.2.0 without config", name, n.staged, n.stagedConfig)
		}
	}
	if out := l.ta.stdout.String(); strings.Count(out, "config not delivered: the admin role cannot deliver config") != 1 {
		t.Errorf("output:\n%s", out)
	}
}

// TestUpgradeAsksForPasswordsBeforeDraining asks for the fallback password of each node whose
// config adds an encrypted volume one node after the other, before any node is drained, also
// when a batch upgrades nodes at once, and sends each node its own.
func TestUpgradeAsksForPasswordsBeforeDraining(t *testing.T) {
	l := newUpgradeLab(t, 1)
	volume := "apiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: data}\nspec: {size: 1G, format: ext4, mountPoint: /srv/data, encryption: {mode: tpm2}}\n"
	for _, name := range []string{"w1", "w2"} {
		path := filepath.Join(l.ta.dir, "nodes", name+".yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, strings.Replace(string(data), n1SystemDisk, passwordSystemDisk, 1)+"---\n"+volume)
	}
	l.ta.answers = []string{"one", "one", "two", "two"}
	read := l.ta.readSecret
	l.ta.readSecret = func(ctx context.Context, prompt string) ([]byte, error) {
		l.record("prompt %s", prompt)
		return read(ctx, prompt)
	}
	if err := l.upgrade(testUpgradeImage(t, "w", "0.2.0"), "--nodes", "w1,w2", "--max-unavailable", "2"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	events := l.takeEvents()
	want := []string{
		"prompt Password that unlocks w1 when its TPM fails: ", "prompt Repeat the password: ",
		"prompt Password that unlocks w2 when its TPM fails: ", "prompt Repeat the password: ",
	}
	if len(events) < len(want) || !slices.Equal(events[:len(want)], want) {
		t.Errorf("events %q, want the prompts first, w1's then w2's", events)
	}
	if v := l.images(); v["w1"] != "0.2.0" || v["w2"] != "0.2.0" {
		t.Errorf("the nodes run %v", v)
	}
	for name, password := range map[string]string{"w1": "one", "w2": "two"} {
		if got := l.nodes[name].secret; got != password {
			t.Errorf("%s was sent the fallback secret %q, want %q", name, got, password)
		}
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

// signTestImage signs the boot loader and UKI of a test image in place, as chalklab sign does.
func signTestImage(t *testing.T, dir, key, cert string) {
	t.Helper()
	if err := signImage(context.Background(), filepath.Join(dir, "chalkos.raw"), filepath.Join(dir, "repart-output.json"), key, cert); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeNeedsTheImageContract refuses an image directory without schemas, which the
// inventory's config is checked against.
func TestUpgradeNeedsTheImageContract(t *testing.T) {
	l := newUpgradeLab(t, 1)
	bare := testUpgradeImage(t, "w", "0.3.0")
	if err := os.RemoveAll(filepath.Join(bare, "schemas")); err != nil {
		t.Fatal(err)
	}
	if err := l.upgrade(bare, "--nodes", "w1", "--no-reboot"); err == nil || !strings.Contains(err.Error(), "has no schemas/") {
		t.Errorf("an image without schemas: %v", err)
	}
}

// TestUpgradeChecksTheConfigBeforeDraining refuses an image the inventory's config of a node does
// not suit before any node is drained or sent the image.
func TestUpgradeChecksTheConfigBeforeDraining(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w", "0.2.0")
	if err := os.Remove(filepath.Join(img, "image.json")); err != nil {
		t.Fatal(err)
	}
	err := l.upgrade(img, "--nodes", "w1")
	if err == nil || !strings.Contains(err.Error(), "its image runs no Kubernetes") {
		t.Fatalf("upgrade = %v, want the config refused for an image without Kubernetes", err)
	}
	if events := l.takeEvents(); len(events) != 0 {
		t.Errorf("the nodes were asked %q", events)
	}
}

// TestUpgradeChecksEveryNodesConfigFirst: when the config of a later node does not suit the
// image, no node is drained or sent it, not even those before it.
func TestUpgradeChecksEveryNodesConfigFirst(t *testing.T) {
	l := newUpgradeLab(t, 1)
	appendDocument(t, filepath.Join(l.ta.dir, "nodes", "w2.yaml"), "apiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Kubelet\nmetadata: {name: kubelet}\nspec: {labels: {kubernetes.io/role: edge}}\n")
	err := l.upgrade(testUpgradeImage(t, "w", "0.2.0"), "--nodes", "w1,w2")
	if err == nil || !strings.Contains(err.Error(), "w2") || !strings.Contains(err.Error(), "kubernetes.io/role") {
		t.Fatalf("upgrade = %v, want w2's config refused", err)
	}
	if events := l.takeEvents(); len(events) != 0 {
		t.Errorf("the nodes were asked %q", events)
	}
}

// TestImageContractRefusesAnOlderImageDirectory: a directory with the cluster file older
// chalkos images carried, and no image.json, is refused rather than read as an image without
// Kubernetes.
func TestImageContractRefusesAnOlderImageDirectory(t *testing.T) {
	dir := t.TempDir()
	copySchemas(t, filepath.Join(dir, "schemas"))
	writeFile(t, filepath.Join(dir, "kubernetes.json"), clusterFile("worker"))
	if _, err := imageContract(dir); err == nil || !strings.Contains(err.Error(), "built by an older chalkos") {
		t.Fatalf("err = %v", err)
	}
	writeFile(t, filepath.Join(dir, "image.json"), testImageJSON)
	if c, err := imageContract(dir); err != nil || c.image == nil {
		t.Fatalf("contract = %+v, %v", c, err)
	}
}

// TestUpgradeChecksTheImage refuses images that name no variant, whose UKI names an image
// version or none, by which systemd-boot would order the entry, or whose UKI the cluster's db
// certificate did not sign, before any node is asked to change.
func TestUpgradeChecksTheImage(t *testing.T) {
	if _, err := exec.LookPath("sbsign"); err != nil {
		t.Skip("sbsign not in PATH")
	}
	l := newUpgradeLab(t, 3)
	dir := t.TempDir()
	dbKey, dbCert := testSigner(t, dir, "db")
	otherKey, otherCert := testSigner(t, dir, "other")
	img := testUpgradeImage(t, "w", "0.2.0")
	signedByOther := testUpgradeImage(t, "w", "0.2.0")
	signTestImage(t, signedByOther, otherKey, otherCert)
	for _, tc := range []struct {
		name string
		img  string
		args []string
		want string
	}{
		{"an unsigned UKI", img, []string{"--sign-cert", dbCert}, "Secure Boot would refuse the image's UKI: the image is not signed"},
		{"a UKI signed by another key", signedByOther, []string{"--sign-cert", dbCert}, "not in db"},
		{"an image version", testImageOf(t, "w", "metal", "0.2.0", 512, func(o map[string]string) { o["IMAGE_VERSION"] = "0.2.0" }), []string{"--sign-key", dbKey, "--sign-cert", dbCert}, "the image's UKI: the UKI's os-release sets IMAGE_VERSION to \"0.2.0\""},
		{"no image version", testImageOf(t, "w", "metal", "0.2.0", 512, func(o map[string]string) { delete(o, "IMAGE_VERSION") }), []string{"--sign-key", dbKey, "--sign-cert", dbCert}, "the image's UKI: the UKI's os-release has no IMAGE_VERSION"},
		{"an image without a variant", testUpgradeImage(t, "", "0.2.0"), []string{"--sign-key", dbKey, "--sign-cert", dbCert}, "the image names no variant or platform in its os-release"},
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

// TestUpgradeRetriesAFailedImage installs an image a node fell back from again with
// --retry-failed, once per run: a second fall back stops the run.
func TestUpgradeRetriesAFailedImage(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.unhealthy["0.3.0"] = true
	img := testUpgradeImage(t, "w", "0.3.0")
	if err := l.upgrade(img); err == nil || !strings.Contains(err.Error(), "rolled back to 010101010101") {
		t.Fatalf("upgrade = %v", err)
	}
	l.takeEvents()
	// Still unhealthy: tried once more, then the run stops.
	if err := l.upgrade(img, "--retry-failed"); err == nil || !strings.Contains(err.Error(), "w1: upgrade to "+shortOf(t, img)+" failed: rolled back to 010101010101") {
		t.Fatalf("a retry of an image that stays unhealthy = %v", err)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"w1 drain", "w1 upgrade", "w1 reboot"}) {
		t.Errorf("events %q", got)
	}
	// Healthy now, as after a transient failure outside the image.
	l.unhealthy["0.3.0"] = false
	if err := l.upgrade(img, "--retry-failed"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if !strings.Contains(l.ta.stdout.String(), "w1: fell back from "+shortOf(t, img)+" before; trying it again") {
		t.Errorf("output:\n%s", l.ta.stdout)
	}
	for _, w := range []string{"w1", "w2", "w3"} {
		if v := l.images()[w]; v != "0.3.0" {
			t.Errorf("%s runs %s", w, v)
		}
	}
}

// TestUpgradeDrainsAsAsked passes --delete-emptydir-data on to the drains and names the pods
// without a controller that stay on the node.
func TestUpgradeDrainsAsAsked(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.upgrade(testUpgradeImage(t, "w", "0.2.0"), "--nodes", "w1", "--delete-emptydir-data"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if !l.emptyDir["w1"] {
		t.Error("w1 was drained keeping emptyDir data")
	}
	if out := l.ta.stdout.String(); !strings.Contains(out, "w1: cordoned, evicted 1 pod, kept 1\n") {
		t.Errorf("output:\n%s", out)
	}
	if out := l.ta.stdout.String(); !strings.Contains(out, "w1: keeps pods without a controller: default/bare") || !strings.Contains(out, "tolerations") {
		t.Errorf("output:\n%s", out)
	}
}

// TestUpgradeStopsAtADrainTimeout stops the run when a drain times out while pods still stop,
// naming them, instead of draining the node again through another control plane.
func TestUpgradeStopsAtADrainTimeout(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.stopping["w1"] = true
	err := l.upgrade(testUpgradeImage(t, "w", "0.2.0"))
	if err == nil || !strings.Contains(err.Error(), "pods default/web still run: they did not stop") {
		t.Fatalf("upgrade = %v", err)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"w1 drain"}) {
		t.Errorf("events %q", got)
	}
}

// TestUpgradeContinuesOnceReady releases a node a run stopped at after it booted the image only
// once its Node is Ready.
func TestUpgradeContinuesOnceReady(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "w", "0.2.0")
	if err := l.upgrade(img, "--nodes", "w1"); err != nil {
		t.Fatal(err)
	}
	// The run stopped before it uncordoned w1, whose Node is not Ready.
	l.cordoned["w1"] = true
	l.notReady["w1"] = true
	l.takeEvents()
	if err := l.upgrade(img, "--nodes", "w1", "--timeout", "200ms"); err == nil || !strings.Contains(err.Error(), "its Node is not Ready") {
		t.Fatalf("upgrade = %v", err)
	}
	if got := l.takeEvents(); len(got) != 0 || !l.cordoned["w1"] {
		t.Errorf("events %q; w1 cordoned %v", got, l.cordoned["w1"])
	}
	l.notReady["w1"] = false
	if err := l.upgrade(img, "--nodes", "w1"); err != nil {
		t.Fatal(err)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"w1 uncordon"}) {
		t.Errorf("events %q", got)
	}
}

// TestUpgradeComparesRootHashes takes a node for one that runs the image only when it runs the
// image's store: one that runs another build gets the image, whatever that build was named.
func TestUpgradeComparesRootHashes(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "w", "0.2.0")
	l.nodes["w1"].image, l.nodes["w1"].root = "0.2.0", bytes.Repeat([]byte{2}, 32)
	if err := l.upgrade(img, "--nodes", "w1"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got := l.takeEvents(); !slices.Contains(got, "w1 upgrade") {
		t.Errorf("events %q, want the image sent to w1", got)
	}
	if n := l.nodes["w1"]; !bytes.Equal(n.root, rootOf(t, img)) {
		t.Errorf("w1 runs the root hash %x", n.root)
	}
}

// onPlatform runs the node on the platform.
func (l *upgradeLab) onPlatform(t *testing.T, name, platform string) {
	l.nodes[name].platform = platform
}

// TestUpgradeSkipsOtherPlatforms installs an image on the nodes of its variant and platform and
// names those of its variant on another platform; a node named on another platform stops the run.
func TestUpgradeSkipsOtherPlatforms(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.onPlatform(t, "w3", "kvm")
	img := testUpgradeImage(t, "w", "0.2.0")
	if err := l.upgrade(img); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if v := l.images(); v["w1"] != "0.2.0" || v["w2"] != "0.2.0" || v["w3"] != "0.1.0" {
		t.Errorf("the workers run %v", v)
	}
	if out := l.ta.stdout.String(); !strings.Contains(out, "skipping w3 (kvm): the image is built for metal") {
		t.Errorf("output:\n%s", out)
	}
	l.takeEvents()
	if err := l.upgrade(img, "--nodes", "w3"); err == nil || !strings.Contains(err.Error(), "w3 runs on kvm; the image is built for metal") {
		t.Errorf("upgrade of w3 = %v", err)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("asked %q", got)
	}
}

// TestUpgradeStopsAtAnotherPlatform stops without --image before any node is sent anything when a
// node runs another platform than its Image's.
func TestUpgradeStopsAtAnotherPlatform(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.nodes["w2"].platform = "kvm"
	l.useInventory(t, map[string]string{"w": testUpgradeImage(t, "w", "0.2.0")})
	want := "w2 runs an image built for kvm, but its Image w-metal is built for metal; changing a node's platform is a reinstall"
	if err := l.run("upgrade", "--nodes", "w1,w2"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("upgrade of the Images = %v, want %q", err, want)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("asked %q", got)
	}
}

// TestUpgradeWithAClientFileAlone installs an image, without an inventory, on the nodes --address
// names that run its variant and platform, after asking each what it runs, and names those of
// its variant on another platform; without --image it is refused, as no Image names the nodes'
// images.
func TestUpgradeWithAClientFileAlone(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.onPlatform(t, "w3", "kvm")
	c, err := client.NewConfig(l.ta.secrets.OSCA, l.ta.secrets.OSCABundle(), "lab", "ops", pki.RoleAdmin, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := c.Encode()
	config := filepath.Join(t.TempDir(), "ops.json")
	writeFile(t, config, string(data))
	addressed := func(args ...string) []string {
		for name, addr := range l.addrs {
			args = append(args, "--address", name+"="+addr)
		}
		return args
	}
	if err := l.ta.run(context.Background(), addressed("upgrade", "--config", config)); err == nil || !strings.Contains(err.Error(), "pass --image") {
		t.Errorf("upgrade with a client file alone, without --image = %v", err)
	}
	if err := l.ta.run(context.Background(), []string{"upgrade", "--image", testUpgradeImage(t, "w", "0.2.0"), "--config", config}); err == nil || !strings.Contains(err.Error(), "--address NODE=ADDR") {
		t.Errorf("upgrade without an inventory or addresses = %v", err)
	}
	// A worker is drained through a control plane, which only an address names.
	err = l.ta.run(context.Background(), []string{"upgrade", "--image", testUpgradeImage(t, "w", "0.2.0"), "--config", config, "--address", "w1=" + l.addrs["w1"]})
	if err == nil || !strings.Contains(err.Error(), "give a control plane's address with --address") {
		t.Errorf("upgrade of a worker without a control plane's address = %v", err)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("asked %q", got)
	}
	if err := l.ta.run(context.Background(), addressed("upgrade", "--image", testUpgradeImage(t, "w", "0.2.0"), "--config", config)); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if v := l.images(); v["w1"] != "0.2.0" || v["w2"] != "0.2.0" || v["w3"] != "0.1.0" || v["cp1"] != "0.1.0" || v["n1"] != "0.1.0" {
		t.Errorf("the nodes run %v", v)
	}
	if out := l.ta.stdout.String(); !strings.Contains(out, "w1: no inventory to compare the config with; the node carries its own over") {
		t.Errorf("output lacks the line without an inventory:\n%s", out)
	}
	if out := l.ta.stdout.String(); !strings.Contains(out, "skipping w3 (kvm): the image is built for metal") {
		t.Errorf("output:\n%s", out)
	}
}

// TestUpgradeNamedNodeOfAnotherVariant gives a named node the image of another variant: an
// upgrade may change a node's variant, and the node checks its stored config against the image.
func TestUpgradeNamedNodeOfAnotherVariant(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.upgrade(testUpgradeImage(t, "w", "0.2.0"), "--nodes", "cp1"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if v := l.images(); v["cp1"] != "0.2.0" || v["cp2"] != "0.1.0" || v["w1"] != "0.1.0" {
		t.Errorf("the nodes run %v", v)
	}
	if want := "cp1 runs the variant cp; it gets the image of w"; !strings.Contains(l.ta.stdout.String(), want) {
		t.Errorf("stdout = %q, want %q", l.ta.stdout, want)
	}
}

// TestUpgradeWithAnImageGivesItToTheNodesOfItsVariant installs the image on the nodes that run
// its variant, and leaves a node of the inventory that runs another one.
func TestUpgradeWithAnImageGivesItToTheNodesOfItsVariant(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.nodes["w2"].variant = "plain"
	if err := l.upgrade(testUpgradeImage(t, "w", "0.2.0")); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if v := l.images(); v["w1"] != "0.2.0" || v["w2"] != "0.1.0" || v["w3"] != "0.2.0" || v["cp1"] != "0.1.0" || v["n1"] != "0.1.0" {
		t.Errorf("the nodes run %v", v)
	}
}

// versionOf is the version of config as delivered; empty for none.
func versionOf(text string) string {
	if text == "" {
		return ""
	}
	docs, err := config.Parse(strings.NewReader(text))
	if err != nil {
		return "unparsable"
	}
	v, _ := config.Hash(docs)
	return v
}

// inventoryConfig is the config the inventory gives the node, as chalkctl delivers it.
func (l *upgradeLab) inventoryConfig(name string) string {
	l.t.Helper()
	c, err := loadInventory(l.inventory)
	if err != nil {
		l.t.Fatal(err)
	}
	nc, err := c.config(name)
	if err != nil {
		l.t.Fatal(err)
	}
	e, err := config.Encode(nc.Documents)
	if err != nil {
		l.t.Fatal(err)
	}
	return e.Text
}

// TestUpgradeSendsTheConfig sends each node the inventory's config with the image, which the node
// keeps for the image.
func TestUpgradeSendsTheConfig(t *testing.T) {
	l := newUpgradeLab(t, 1)
	if err := l.upgrade(testUpgradeImage(t, "w", "0.2.0"), "--nodes", "w1", "--no-reboot"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got := l.nodes["w1"].stagedConfig; got == "" || got != l.inventoryConfig("w1") {
		t.Errorf("w1 was sent the config %q, want the inventory's", got)
	}
}

// TestUpgradeSendsAStagedImageAgainForItsConfig sends a staged image again when the config kept
// for it is not the inventory's, so the node replaces it, and leaves it when it is.
func TestUpgradeSendsAStagedImageAgainForItsConfig(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w", "0.2.0")
	l.nodes["w1"].staged, l.nodes["w1"].stagedRoot, l.nodes["w1"].stagedConfig = "0.2.0", rootOf(t, img), "{}"
	if err := l.upgrade(img, "--nodes", "w1", "--no-reboot"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"w1 upgrade"}) {
		t.Errorf("events %q, want the image sent again", got)
	}
	if got := l.nodes["w1"].stagedConfig; got != l.inventoryConfig("w1") {
		t.Errorf("w1 keeps the config %q, want the inventory's", got)
	}
	if err := l.upgrade(img, "--nodes", "w1", "--no-reboot"); err != nil {
		t.Fatal(err)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("events %q, want the staged image left alone", got)
	}
}

// ApplyConfig keeps the config for the running image, or with staged for the staged one, which
// a node without one refuses.
func (n *upgradeFake) ApplyConfig(ctx context.Context, req *connect.Request[nodev1.ApplyConfigRequest]) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	if err := needsAdmin(ctx, "ApplyConfig"); err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	switch {
	case !req.Msg.Staged:
		n.config = req.Msg.Config
	case n.staged == "":
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no image is staged to boot next"))
	default:
		n.stagedConfig = req.Msg.Config
	}
	n.lab.record("%s apply-config staged=%v", n.name, req.Msg.Staged)
	return connect.NewResponse(&nodev1.ApplyConfigResponse{}), nil
}

// Contract is the contract of the image the node runs: the fixture schemas and, on the lab's
// Kubernetes variants, image.json.
func (n *upgradeFake) Contract(context.Context, *connect.Request[nodev1.ContractRequest]) (*connect.Response[nodev1.ContractResponse], error) {
	resp := &nodev1.ContractResponse{Platform: n.platform, Schemas: map[string][]byte{}}
	for _, dir := range fixtureSchemas {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if resp.Schemas[e.Name()], err = os.ReadFile(filepath.Join(dir, e.Name())); err != nil {
				return nil, err
			}
		}
	}
	if n.variant == "cp" || n.variant == "w" || n.variant == "k8s" {
		resp.KubernetesImage = []byte(testImageJSON)
	}
	return connect.NewResponse(resp), nil
}

// applyConfig runs chalkctl apply-config on the lab's node with the args given.
func (l *upgradeLab) applyConfig(name string, args ...string) error {
	l.ta.stdout.Reset()
	l.ta.stderr.Reset()
	return l.ta.run(context.Background(), append([]string{"apply-config", name, "--inventory", l.inventory, "--address", l.addrs[name]}, args...))
}

// TestApplyConfigStaged delivers the inventory's config for the staged image, checked against the
// image the node's Image names, and the running image's config warns of the staged one; status
// compares both with the inventory.
func TestApplyConfigStaged(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w", "0.2.0")
	l.useInventory(t, map[string]string{"w": img})
	n := l.nodes["w1"]
	if err := l.applyConfig("w1", "--staged"); err == nil || !strings.Contains(err.Error(), "no image is staged") {
		t.Fatalf("apply-config --staged without a staged image = %v", err)
	}
	n.staged, n.stagedRoot, n.stagedConfig = "0.2.0", rootOf(t, img), "{}"
	if err := l.applyConfig("w1", "--staged"); err != nil {
		t.Fatal(err)
	}
	want := versionOf(l.inventoryConfig("w1"))
	if n.stagedConfig != l.inventoryConfig("w1") || n.config != "" {
		t.Errorf("w1 keeps %q for the staged image and %q for the running one", n.stagedConfig, n.config)
	}
	if !strings.Contains(l.ta.stdout.String(), "w1 boots config "+want+" with its staged image") {
		t.Errorf("stdout = %q", l.ta.stdout)
	}
	if err := l.applyConfig("w1"); err != nil {
		t.Fatal(err)
	}
	if warning := "w1 has a staged image with its own config (version " + want + "); apply-config --staged updates it"; !strings.Contains(l.ta.stderr.String(), warning) {
		t.Errorf("stderr = %q, want %q", l.ta.stderr, warning)
	}
	n.stagedConfig = "{}"
	l.ta.stdout.Reset()
	if err := l.ta.run(context.Background(), []string{"status", "w1", "--inventory", l.inventory, "--address", l.addrs["w1"]}); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"config " + want + " (the inventory's)", "staged config " + versionOf("{}") + " (not the inventory's; chalkctl apply-config --staged w1 delivers it)"} {
		if !strings.Contains(l.ta.stdout.String(), line) {
			t.Errorf("status lacks %q:\n%s", line, l.ta.stdout)
		}
	}
}

// GetConfig returns the config the node runs, or with staged the staged image's, redacted as
// chalkd redacts it.
func (n *upgradeFake) GetConfig(ctx context.Context, req *connect.Request[nodev1.GetConfigRequest]) (*connect.Response[nodev1.GetConfigResponse], error) {
	n.lab.mu.Lock()
	denied := n.lab.configDenied
	n.lab.mu.Unlock()
	if denied {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("GetConfig needs the admin role"))
	}
	if err := needsAdmin(ctx, "GetConfig"); err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	text := n.config
	if req.Msg.Staged {
		if n.staged == "" {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no image is staged to boot next"))
		}
		text = n.stagedConfig
	}
	docs, err := config.Parse(strings.NewReader(text))
	if err != nil {
		return nil, err
	}
	e, err := config.Encode(config.Redact(docs))
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		e.Text = ""
	}
	return connect.NewResponse(&nodev1.GetConfigResponse{Documents: e.Text, Version: versionOf(text)}), nil
}

// TestApplyConfigPrintsWhatItChanges prints a line of what the config changes before it is
// delivered, and with --diff the whole diff, delivering nothing.
func TestApplyConfigPrintsWhatItChanges(t *testing.T) {
	l := newUpgradeLab(t, 1)
	l.useInventory(t, map[string]string{"w": testUpgradeImage(t, "w", "0.2.0")})
	n := l.nodes["w1"]
	if err := l.applyConfig("w1"); err != nil {
		t.Fatal(err)
	}
	if want := "w1: config 8 added, 0 removed, 0 changed\n"; !strings.Contains(l.ta.stdout.String(), want) {
		t.Errorf("stdout = %q, want %q", l.ta.stdout, want)
	}
	l.takeEvents()
	n.config = strings.Replace(n.config, "10.0.0.13/24", "10.0.0.99/24", 1) + "---\n" + `{"apiVersion":"storage.chalkos.dev/v1alpha1","kind":"Volume","metadata":{"name":"old"},"spec":{}}` + "\n"
	if err := l.applyConfig("w1", "--diff"); err != nil {
		t.Fatal(err)
	}
	want := `w1: config 0 added, 1 removed (Volume "old"), 1 changed
~ Network "10-uplink"
    spec.network.address[0]: "10.0.0.99/24" → "10.0.0.13/24"
- Volume "old" (on the node, not in the inventory)
`
	if l.ta.stdout.String() != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", l.ta.stdout, want)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("--diff delivered: %q", got)
	}
}

// TestUpgradeDiff prints each node's diff against the config it runs and changes nothing; an
// upgrade prints the line of it before it sends the image.
func TestUpgradeDiff(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w", "0.2.0")
	l.nodes["w1"].config = l.inventoryConfig("w1")
	if err := l.upgrade(img, "--diff"); err != nil {
		t.Fatal(err)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("--diff asked %q", got)
	}
	out := l.ta.stdout.String()
	if !strings.Contains(out, "w1: config unchanged\n") || !strings.Contains(out, "w2: config 8 added, 0 removed, 0 changed\n+ Cluster \"lab\"\n") {
		t.Errorf("output:\n%s", out)
	}
	l.ta.stdout.Reset()
	if err := l.upgrade(img, "--nodes", "w1", "--no-reboot"); err != nil {
		t.Fatal(err)
	}
	if out := l.ta.stdout.String(); !strings.Contains(out, "w1: config unchanged\nw1: installed ") {
		t.Errorf("output:\n%s", out)
	}
}
