package chalkctl

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/image/imagetest"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/ociimage/ocitest"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/schema"
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
	imgDir := filepath.Join(t.TempDir(), "image")
	root := imagetest.Write(t, imgDir, imagetest.Image{Variant: variant, Platform: platform, Seed: name, BlockSize: blockSize, Edit: edit})
	testImageNames.Store(string(root), name)
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
	// layers are the layers of the image the node runs, as Info reports them.
	layers []string

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
	// header is the header the last upgrade sent.
	header *nodev1.UpgradeHeader
	// previous, previousRoot and previousConfig are the image an upgrade replaced, which the node
	// can roll back to, and its config.
	previous       string
	previousRoot   []byte
	previousConfig string
	// rolledBackTo is the root hash of the image the last rollback rolled back to, as chalkd records
	// it; an upgrade that stages an image removes it.
	rolledBackTo []byte
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
	// refuseRollback makes the node refuse its next rollback, as if its UKI could not be renamed.
	refuseRollback string
	// uncountedRollback makes the nodes stage a rollback's image without tries, as chalkd does while
	// the boot a node runs was not found healthy.
	uncountedRollback bool
	// emptyDir holds the nodes drained deleting emptyDir data.
	emptyDir map[string]bool
	// notReady holds the nodes whose Node is not Ready.
	notReady map[string]bool
	// stopping holds the nodes whose drains time out while their pods stop.
	stopping map[string]bool
	// configDenied makes the nodes refuse GetConfig, as for a role below admin.
	configDenied bool
	// configErrs makes single nodes fail GetConfig with the error.
	configErrs map[string]error
	// configAsked counts the GetConfig calls of each node.
	configAsked map[string]int
}

// asked returns how often each node was asked for its config.
func (l *upgradeLab) asked() map[string]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return maps.Clone(l.configAsked)
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
	return connect.NewResponse(&nodev1.InfoResponse{RootHash: n.root, ImageId: "chalkos", Variant: n.variant, Platform: n.platform, Layers: n.layers}), nil
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
	if n.previous != "" {
		st.Boot.Previous = &nodev1.BootImage{RootHash: n.previousRoot, Generation: n.generation - 1}
	}
	if n.rolledBackTo != nil && (bytes.Equal(n.rolledBackTo, n.root) || bytes.Equal(n.rolledBackTo, n.stagedRoot)) {
		st.Boot.Rollback = &nodev1.BootImage{RootHash: n.rolledBackTo}
	}
	if n.failed != "" {
		st.Boot.Failed = &nodev1.BootImage{RootHash: n.failedRoot, Generation: n.generation + 1}
	}
	st.ConfigVersion = versionOf(n.config)
	if n.failed != "" {
		st.Boot.Journal = []string{"chalkd: not healthy yet: units failed: broken.service"}
	}
	return connect.NewResponse(st), nil
}

// upgradeKubernetes is an upgradeFake's Kubernetes layer.
type upgradeKubernetes struct {
	*upgradeFake
	kubernetesv1connect.UnimplementedKubernetesServiceHandler
}

func (n upgradeKubernetes) Status(context.Context, *connect.Request[kubernetesv1.StatusRequest]) (*connect.Response[kubernetesv1.StatusResponse], error) {
	if err := n.down(); err != nil {
		return nil, err
	}
	n.lab.mu.Lock()
	ready := "True"
	if n.lab.notReady[n.name] {
		ready = "False"
	}
	n.lab.mu.Unlock()
	switch n.kind {
	case kubernetes.KindControlPlane:
		return connect.NewResponse(&kubernetesv1.StatusResponse{Kind: n.kind, State: "bootstrapped", Phase: kubernetesv1.Phase_PHASE_BOOTSTRAPPED, NodeReady: ready}), nil
	case kubernetes.KindWorker:
		return connect.NewResponse(&kubernetesv1.StatusResponse{Kind: n.kind, State: "joined", Phase: kubernetesv1.Phase_PHASE_JOINED, NodeReady: ready}), nil
	}
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("the kubernetes layer is not in this image"))
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
	if ref := header.GetSource().GetOci().GetReference(); ref != "" {
		if received != 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("an upgrade that pulls its image carries no chunks"))
		}
		n.lab.record("%s pull %s", n.name, ref)
		received = h.Store.Size + h.HashTree.Size + h.Uki.Size
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
	n.previous, n.previousRoot, n.previousConfig, n.rolledBackTo = "", nil, "", nil
	n.stagedConfig, n.secret = header.GetConfig(), header.GetFallbackSecret()
	n.stagedStoreSize, n.stagedVeritySize = h.Store.Size, h.HashTree.Size
	n.header = header
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
			n.previous, n.previousRoot, n.previousConfig = n.image, n.root, n.config
			n.image, n.root, n.staged, n.stagedRoot = n.staged, n.stagedRoot, "", nil
			n.config, n.stagedConfig = n.stagedConfig, ""
			n.generation++
		}
	}()
	return connect.NewResponse(&nodev1.RebootResponse{}), nil
}

// RollbackImage stages the image an upgrade replaced and records it, as chalkd re-activates it.
func (n *upgradeFake) RollbackImage(_ context.Context, req *connect.Request[nodev1.RollbackImageRequest]) (*connect.Response[nodev1.RollbackImageResponse], error) {
	if err := n.down(); err != nil {
		return nil, err
	}
	n.lab.record("%s rollback", n.name)
	n.lab.mu.Lock()
	refuse := n.lab.refuseRollback == n.name
	if refuse {
		n.lab.refuseRollback = ""
	}
	entry := "chalkos_%d+3.efi"
	if n.lab.uncountedRollback {
		entry = "chalkos_%d.efi"
	}
	n.lab.mu.Unlock()
	if refuse {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("roll back: name the UKI chalkos_3+3.efi: input/output error"))
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	switch {
	case bytes.Equal(req.Msg.RootHash, n.root):
		return connect.NewResponse(&nodev1.RollbackImageResponse{AlreadyInstalled: true}), nil
	case n.previous == "" || !bytes.Equal(req.Msg.RootHash, n.previousRoot):
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("roll back: the node keeps no image it ran before to roll back to"))
	}
	n.staged, n.stagedRoot, n.stagedConfig = n.previous, n.previousRoot, n.previousConfig
	n.previous, n.previousRoot, n.previousConfig = "", nil, ""
	n.rolledBackTo = req.Msg.RootHash
	return connect.NewResponse(&nodev1.RollbackImageResponse{Entry: fmt.Sprintf(entry, n.generation+1)}), nil
}

func (n upgradeKubernetes) EtcdMembers(context.Context, *connect.Request[kubernetesv1.EtcdMembersRequest]) (*connect.Response[kubernetesv1.EtcdMembersResponse], error) {
	if err := n.down(); err != nil {
		return nil, err
	}
	if n.kind != kubernetes.KindControlPlane {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the node is a worker; ask a control-plane node"))
	}
	n.lab.record("%s members", n.name)
	resp := &kubernetesv1.EtcdMembersResponse{}
	n.lab.mu.Lock()
	defer n.lab.mu.Unlock()
	for name, node := range n.lab.nodes {
		if node.kind == kubernetes.KindControlPlane {
			m := &kubernetesv1.EtcdMember{Name: name}
			if slices.Contains(n.lab.unhealthyMembers, name) {
				m.Unhealthy = "no answer"
			}
			resp.Members = append(resp.Members, m)
		}
	}
	return connect.NewResponse(resp), nil
}

func (n upgradeKubernetes) DrainNode(_ context.Context, req *connect.Request[kubernetesv1.DrainNodeRequest]) (*connect.Response[kubernetesv1.DrainNodeResponse], error) {
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
	return connect.NewResponse(&kubernetesv1.DrainNodeResponse{Marked: marked, Evicted: []string{"default/web"}, Unmanaged: []string{"default/bare"}}), nil
}

func (n upgradeKubernetes) UncordonNode(_ context.Context, req *connect.Request[kubernetesv1.UncordonNodeRequest]) (*connect.Response[kubernetesv1.UncordonNodeResponse], error) {
	if err := n.down(); err != nil {
		return nil, err
	}
	n.lab.mu.Lock()
	defer n.lab.mu.Unlock()
	if !n.lab.cordoned[req.Msg.Node] {
		return connect.NewResponse(&kubernetesv1.UncordonNodeResponse{}), nil
	}
	delete(n.lab.cordoned, req.Msg.Node)
	n.lab.events = append(n.lab.events, req.Msg.Node+" uncordon")
	return connect.NewResponse(&kubernetesv1.UncordonNodeResponse{Uncordoned: true}), nil
}

// fakeRoleKey holds the role a fake node's client certificate grants.
type fakeRoleKey struct{}

// withRoles serves h with the role of each request's client certificate in its context, as chalkd
// derives it from the certificate's Organization.
func withRoles(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var role pki.Role
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			role, _ = pki.RoleOf(r.TLS.PeerCertificates[0])
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), fakeRoleKey{}, role)))
	})
}

// needsAdmin refuses what only an admin may do, as chalkd does.
func needsAdmin(ctx context.Context, what string) error {
	if role, _ := ctx.Value(fakeRoleKey{}).(pki.Role); !role.Allows(pki.RoleAdmin) {
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
		nodes = append(nodes, testNode{fmt.Sprintf("cp%d", i), kubernetes.KindControlPlane, cpVariant})
	}
	for _, w := range workers {
		nodes = append(nodes, testNode{w, kubernetes.KindWorker, wVariant})
	}
	nodes = append(nodes, testNode{"n1", "", "plain"})
	l := &upgradeLab{t: t, ta: ta, nodes: map[string]*upgradeFake{}, addrs: map[string]string{}, unhealthy: map[string]bool{}, cordoned: map[string]bool{}, emptyDir: map[string]bool{}, stopping: map[string]bool{}, notReady: map[string]bool{}}
	for _, node := range nodes {
		name := node.name
		n := &upgradeFake{lab: l, name: name, variant: node.variant, platform: "metal", kind: node.kind, image: "0.1.0", root: bytes.Repeat([]byte{1}, 32), generation: 1}
		if node.kind != "" {
			n.layers = []string{layers.Kubernetes}
		}
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
		mux := http.NewServeMux()
		mux.Handle(nodev1connect.NewNodeServiceHandler(n))
		mux.Handle(kubernetesv1connect.NewKubernetesServiceHandler(upgradeKubernetes{upgradeFake: n}))
		l.addrs[name] = serveTLS(t, withRoles(mux), chalkd.TLSConfig(nc.GetCertificate, nc.ClientCAs))
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
// each node runs the image of its variant on metal, whose directory images gives by variant.
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
// image the inventory names for it, one image per directory, the control planes first; a node
// that runs another platform than its image stops the run.
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
	if err := l.run("upgrade", "--nodes", "w3"); err == nil || !strings.Contains(err.Error(), "w3 runs an image built for kvm, but its image w-metal is built for metal") {
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

// TestUpgradeFromARegistry upgrades a node whose image the inventory names in a registry by tag:
// the node is told to pull the image by the digest chalkctl resolved, with the header a directory
// of the same build gives and no chunks.
func TestUpgradeFromARegistry(t *testing.T) {
	ctx := context.Background()
	dir := testUpgradeImage(t, "plain", "0.2.0")
	writeFile(t, filepath.Join(dir, "repart.d", "00-esp.conf"), "[Partition]\nType=esp\n")
	img, closeImage, err := ociimage.FromDirectory(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	ref := ocitest.Registry(t) + "/lab/plain-metal:v2"
	pinned := ocitest.Push(t, img, ref)
	closeImage()

	l := newUpgradeLab(t, 1)
	l.useInventory(t, map[string]string{"plain": "oci:" + ref})
	if err := l.run("upgrade", "--nodes", "n1"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if !slices.Contains(l.takeEvents(), "n1 pull "+pinned) {
		t.Errorf("n1 was not told to pull %s", pinned)
	}
	if v := l.images()["n1"]; v != "0.2.0" {
		t.Errorf("n1 runs %s", v)
	}
	if want := "n1: installing " + shortOf(t, dir) + ", which it pulls from " + pinned; !strings.Contains(l.ta.stdout.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, l.ta.stdout)
	}
	streamed, err := openImage(ctx, dir, false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer streamed.Close()
	if got := l.nodes["n1"].header.GetImage(); !proto.Equal(got, streamed.header) {
		t.Errorf("header %v, want the directory's %v", got, streamed.header)
	}
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

// TestUpgradeWithAnOperatorClientFileAlone upgrades a partial cluster as an operator: there is no
// inventory, so no config to withhold, and each node says it carries its own over.
func TestUpgradeWithAnOperatorClientFileAlone(t *testing.T) {
	l := newUpgradeLab(t, 1)
	config := l.ta.writeConfig(t, "ops", pki.RoleOperator, time.Hour, time.Now())
	args := []string{"upgrade", "--image", testUpgradeImage(t, "w", "0.2.0"), "--config", config, "--nodes", "w1", "--no-reboot"}
	for name, addr := range l.addrs {
		args = append(args, "--address", name+"="+addr)
	}
	if err := l.ta.run(context.Background(), args); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	out := l.ta.stdout.String()
	if !strings.Contains(out, "w1: no inventory to compare the config with; the node carries its own over") || strings.Contains(out, "config not delivered") {
		t.Errorf("output:\n%s\nwant the line without an inventory and none about withheld config", out)
	}
	if n := l.nodes["w1"]; n.staged != "0.2.0" || n.stagedConfig != "" {
		t.Errorf("w1 stages %q with the config %q, want 0.2.0 without config", n.staged, n.stagedConfig)
	}
}

// TestUpgradeWithAReaderClientFile refuses an upgrade below the operator role before any node is
// asked anything, as the nodes would refuse it only once a worker is drained.
func TestUpgradeWithAReaderClientFile(t *testing.T) {
	l := newUpgradeLab(t, 1)
	config := l.ta.writeConfig(t, "viewer", pki.RoleReader, time.Hour, time.Now())
	l.ta.withoutSecrets(t)
	err := l.upgrade(testUpgradeImage(t, "w", "0.2.0"), "--config", config, "--nodes", "w1")
	if err == nil || !strings.Contains(err.Error(), "upgrade needs the operator role; the reader role may not upgrade nodes") {
		t.Fatalf("upgrade as a reader = %v", err)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("events %q, want none", got)
	}
}

// TestUpgradeWithholdsConfigANodeDoesNotShow sends no config to nodes that refuse to show theirs
// to the credentials, as they would refuse config with the upgrade, and says so once; --diff stops
// there instead. The credentials read as admin, so the line names the node, not the role.
func TestUpgradeWithholdsConfigANodeDoesNotShow(t *testing.T) {
	l := newUpgradeLab(t, 1)
	l.configDenied = true
	img := testUpgradeImage(t, "w", "0.2.0")
	err := l.upgrade(img, "--diff")
	if err == nil || !strings.Contains(err.Error(), "w1 refuses to show its config to these credentials; upgrade --diff") {
		t.Errorf("upgrade --diff = %v, want w1's refusal named", err)
	}
	if err := l.upgrade(img, "--nodes", "w1,w2", "--no-reboot", "--max-unavailable", "2"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	for _, name := range []string{"w1", "w2"} {
		if n := l.nodes[name]; n.staged != "0.2.0" || n.stagedConfig != "" {
			t.Errorf("%s stages %q with the config %q, want 0.2.0 without config", name, n.staged, n.stagedConfig)
		}
	}
	out := l.ta.stdout.String()
	if line := "w1 refuses to show its config to these credentials; config not delivered, each node carries its own over\n"; strings.Count(out, line) != 1 || strings.Contains(out, "role cannot deliver config") {
		t.Errorf("output:\n%s\nwant %q once", out, line)
	}
}

// TestUpgradeWithholdsConfigBeforeDraining asks the first node to install for its config before
// any password is asked for or any node is drained. Once it refuses to show it, the run withholds
// config from the start, so a node with the image staged is not sent it again for its config and
// no password is asked for.
func TestUpgradeWithholdsConfigBeforeDraining(t *testing.T) {
	l := newUpgradeLab(t, 1)
	l.configDenied = true
	img := testUpgradeImage(t, "w", "0.2.0")
	withPasswordVolume(t, l, "w1", "w2")
	prompts := recordPrompts(l)
	l.nodes["w1"].staged, l.nodes["w1"].stagedRoot, l.nodes["w1"].stagedConfig = "0.2.0", rootOf(t, img), "{}"
	if err := l.upgrade(img, "--nodes", "w1,w2"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	events := l.takeEvents()
	if slices.Contains(events, "w1 upgrade") || !slices.Contains(events, "w2 upgrade") {
		t.Errorf("events %q, want the image sent to w2 alone", events)
	}
	if len(*prompts) != 0 {
		t.Errorf("asked %q", *prompts)
	}
	if out := l.ta.stdout.String(); !strings.Contains(out, "w1 refuses to show its config to these credentials") {
		t.Errorf("output:\n%s", out)
	}
	if v := l.images(); v["w1"] != "0.2.0" || v["w2"] != "0.2.0" {
		t.Errorf("the nodes run %v", v)
	}
}

// TestUpgradeAsksTheFirstNodeItInstallsForItsConfig skips a node that runs the image already, as
// it is not sent the image, and asks the next one for its config. That node refuses to show it, so
// the run withholds config before it asks for the password the node's new volume needs.
func TestUpgradeAsksTheFirstNodeItInstallsForItsConfig(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w", "0.2.0")
	withPasswordVolume(t, l, "w1", "w2")
	prompts := recordPrompts(l)
	l.nodes["w1"].image, l.nodes["w1"].root = "0.2.0", rootOf(t, img)
	l.configErrs = map[string]error{"w2": connect.NewError(connect.CodePermissionDenied, errors.New("GetConfig needs the admin role"))}
	if err := l.upgrade(img, "--nodes", "w1,w2", "--no-reboot"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if asked := l.asked(); asked["w2"] == 0 {
		t.Errorf("asked %v for their config, want w2", asked)
	}
	if len(*prompts) != 0 {
		t.Errorf("asked %q", *prompts)
	}
	if line := "w2 refuses to show its config to these credentials; config not delivered, each node carries its own over\n"; strings.Count(l.ta.stdout.String(), line) != 1 {
		t.Errorf("output:\n%s\nwant %q once", l.ta.stdout, line)
	}
	if n := l.nodes["w2"]; n.staged != "0.2.0" || n.stagedConfig != "" {
		t.Errorf("w2 stages %q with the config %q, want 0.2.0 without config", n.staged, n.stagedConfig)
	}
}

// TestUpgradeStopsWhenTheFirstNodeCannotShowItsConfig stops the run when the first node it
// installs cannot be asked for its config, before any password is asked for or any node drained.
func TestUpgradeStopsWhenTheFirstNodeCannotShowItsConfig(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w", "0.2.0")
	withPasswordVolume(t, l, "w1")
	prompts := recordPrompts(l)
	l.configErrs = map[string]error{"w1": connect.NewError(connect.CodeUnavailable, errors.New("the node is busy"))}
	err := l.upgrade(img, "--nodes", "w1")
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("upgrade = %v, want w1's error", err)
	}
	if len(*prompts) != 0 {
		t.Errorf("asked %q", *prompts)
	}
	if events := l.takeEvents(); len(events) != 0 {
		t.Errorf("events %q, want none", events)
	}
	if n := l.nodes["w1"]; n.staged != "" {
		t.Errorf("w1 stages %q", n.staged)
	}
}

// TestUpgradeAsksNoPasswordOfANodeItDoesNotInstall asks no fallback password of a node that runs
// the image already, nor of one that has it staged with the inventory's config, though their config
// adds an encrypted volume: neither is sent the image.
func TestUpgradeAsksNoPasswordOfANodeItDoesNotInstall(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w", "0.2.0")
	withPasswordVolume(t, l, "w1", "w2")
	prompts := recordPrompts(l)
	l.nodes["w1"].image, l.nodes["w1"].root = "0.2.0", rootOf(t, img)
	l.nodes["w2"].staged, l.nodes["w2"].stagedRoot, l.nodes["w2"].stagedConfig = "0.2.0", rootOf(t, img), l.inventoryConfig("w2")
	if err := l.upgrade(img, "--nodes", "w1,w2", "--no-reboot"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if len(*prompts) != 0 {
		t.Errorf("asked %q", *prompts)
	}
	if events := l.takeEvents(); slices.Contains(events, "w1 upgrade") || slices.Contains(events, "w2 upgrade") {
		t.Errorf("events %q, want no image sent", events)
	}
}

// withPasswordVolume gives the nodes' config a fallback password and an encrypted volume the
// nodes do not have yet.
func withPasswordVolume(t *testing.T, l *upgradeLab, names ...string) {
	t.Helper()
	volume := "apiVersion: storage.chalkos.dev/v1alpha1\nkind: Volume\nmetadata: {name: data}\nspec: {size: 1G, format: ext4, mountPoint: /srv/data, encryption: {mode: tpm2}}\n"
	for _, name := range names {
		path := filepath.Join(l.ta.dir, "nodes", name+".yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, strings.Replace(string(data), n1SystemDisk, passwordSystemDisk, 1)+"---\n"+volume)
	}
}

// recordPrompts records each prompt on the terminal as an event of the lab, and returns them all.
func recordPrompts(l *upgradeLab) *[]string {
	var prompts []string
	read := l.ta.readSecret
	l.ta.readSecret = func(ctx context.Context, prompt string) ([]byte, error) {
		l.record("prompt %s", prompt)
		l.mu.Lock()
		prompts = append(prompts, prompt)
		l.mu.Unlock()
		return read(ctx, prompt)
	}
	return &prompts
}

// TestUpgradeAsksForPasswordsBeforeDraining asks for the fallback password of each node whose
// config adds an encrypted volume one node after the other, before any node is drained, also
// when a batch upgrades nodes at once, and sends each node its own.
func TestUpgradeAsksForPasswordsBeforeDraining(t *testing.T) {
	l := newUpgradeLab(t, 1)
	withPasswordVolume(t, l, "w1", "w2")
	l.ta.answers = []string{"one", "one", "two", "two"}
	recordPrompts(l)
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
// not suit before any node is drained or sent the image: one without Kubernetes, which accepts no
// Node.
func TestUpgradeChecksTheConfigBeforeDraining(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w", "0.2.0")
	for _, f := range []string{"image.json", filepath.Join("schemas", "kubernetes.chalkos.dev_v1alpha1_Node.json")} {
		if err := os.Remove(filepath.Join(img, f)); err != nil {
			t.Fatal(err)
		}
	}
	err := l.upgrade(img, "--nodes", "w1")
	if err == nil || !strings.Contains(err.Error(), `does not accept kind "Node"`) {
		t.Fatalf("upgrade = %v, want the config refused for an image without Kubernetes", err)
	}
	if events := l.takeEvents(); len(events) != 0 {
		t.Errorf("the nodes were asked %q", events)
	}
}

// TestUpgradeOfAChalkdWithoutTheLayersAPI says to upgrade a chalkd whose Info names the
// Kubernetes layer but which serves no API of it, before the node is sent anything.
func TestUpgradeOfAChalkdWithoutTheLayersAPI(t *testing.T) {
	l := newUpgradeLab(t, 1)
	l.nodes["n1"].layers = []string{layers.Kubernetes}
	err := l.upgrade(testUpgradeImage(t, "plain", "0.2.0"), "--nodes", "n1")
	if want := "n1: the node's chalkd serves no API of the kubernetes layer; upgrade chalkd first"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("upgrade = %v, want %q", err, want)
	}
	if events := l.takeEvents(); len(events) != 0 {
		t.Errorf("the nodes were asked %q", events)
	}
}

// TestUpgradeChecksEveryNodesConfigFirst: when the config of a later node does not suit the
// image, no node is drained or sent it, not even those before it.
func TestUpgradeChecksEveryNodesConfigFirst(t *testing.T) {
	l := newUpgradeLab(t, 1)
	path := filepath.Join(l.ta.dir, "nodes", "w2.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, strings.Replace(string(data), "spec: {type: worker}", "spec: {type: worker, labels: {kubernetes.io/role: edge}}", 1))
	err = l.upgrade(testUpgradeImage(t, "w", "0.2.0"), "--nodes", "w1,w2")
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
	if c, err := imageContract(dir); err != nil || c.target.Image == nil {
		t.Fatalf("contract = %+v, %v", c, err)
	}
}

// TestUpgradeChecksTheImage refuses images that name no variant, whose UKI names an image
// version or none, by which systemd-boot would order the entry, before any node is asked to
// change.
func TestUpgradeChecksTheImage(t *testing.T) {
	l := newUpgradeLab(t, 3)
	for _, tc := range []struct {
		name string
		img  string
		want string
	}{
		{"an image version", testImageOf(t, "w", "metal", "0.2.0", 512, func(o map[string]string) { o["IMAGE_VERSION"] = "0.2.0" }), "the image's UKI: the UKI's os-release sets IMAGE_VERSION to \"0.2.0\""},
		{"no image version", testImageOf(t, "w", "metal", "0.2.0", 512, func(o map[string]string) { delete(o, "IMAGE_VERSION") }), "the image's UKI: the UKI's os-release has no IMAGE_VERSION"},
		{"an image without a variant", testUpgradeImage(t, "", "0.2.0"), "the image names no variant or platform in its os-release"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := l.upgrade(tc.img)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("upgrade = %v, want %q", err, tc.want)
			}
			if got := l.takeEvents(); len(got) != 0 {
				t.Errorf("asked %q", got)
			}
		})
	}
	if err := l.upgrade(testUpgradeImage(t, "w", "0.2.0")); err != nil {
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

// TestUpgradeDrainsOnlyNodesWithTheKubernetesLayer upgrades a worker whose image carries the
// router layer as a node without Kubernetes: it is neither drained nor uncordoned, whatever its
// status says of Kubernetes.
func TestUpgradeDrainsOnlyNodesWithTheKubernetesLayer(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.nodes["w1"].layers = []string{"router"}
	if err := l.upgrade(testUpgradeImage(t, "w", "0.2.0"), "--nodes", "w1"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	events := l.takeEvents()
	if !slices.Contains(events, "w1 upgrade") {
		t.Errorf("w1 was not upgraded: %q", events)
	}
	if slices.Contains(events, "w1 drain") || slices.Contains(events, "w1 uncordon") {
		t.Errorf("a node without the Kubernetes layer was drained: %q", events)
	}
}

// TestUpgradeAsksOnlyControlPlanesWithTheKubernetesLayer upgrades a worker of a partial cluster
// through the control plane --address names, which the run leaves alone, only when that node's
// image has the Kubernetes layer.
func TestUpgradeAsksOnlyControlPlanesWithTheKubernetesLayer(t *testing.T) {
	l := newUpgradeLab(t, 1)
	config := l.ta.writeConfig(t, "ops", pki.RoleOperator, time.Hour, time.Now())
	upgrade := func() error {
		args := []string{"upgrade", "--image", testUpgradeImage(t, "w", "0.2.0"), "--config", config, "--nodes", "w1"}
		for _, name := range []string{"cp1", "w1"} {
			args = append(args, "--address", name+"="+l.addrs[name])
		}
		return l.ta.run(context.Background(), args)
	}
	l.nodes["cp1"].layers = []string{"router"}
	if err := upgrade(); err == nil || !strings.Contains(err.Error(), "give a control plane's address with --address") {
		t.Errorf("upgrade through a node without the Kubernetes layer = %v", err)
	}
	l.nodes["cp1"].layers = []string{layers.Kubernetes}
	if err := upgrade(); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if events := l.takeEvents(); !slices.Contains(events, "w1 drain") {
		t.Errorf("w1 was not drained: %q", events)
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
// node runs another platform than its image.
func TestUpgradeStopsAtAnotherPlatform(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.nodes["w2"].platform = "kvm"
	l.useInventory(t, map[string]string{"w": testUpgradeImage(t, "w", "0.2.0")})
	want := "w2 runs an image built for kvm, but its image w-metal is built for metal; changing a node's platform is a reinstall"
	if err := l.run("upgrade", "--nodes", "w1,w2"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("upgrade of the inventory's images = %v, want %q", err, want)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("asked %q", got)
	}
}

// TestUpgradeWithAClientFileAlone installs an image, without an inventory, on the nodes --address
// names that run its variant and platform, after asking each what it runs, and names those of
// its variant on another platform; without --image it is refused, as no inventory names the
// nodes' images.
func TestUpgradeWithAClientFileAlone(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.onPlatform(t, "w3", "kvm")
	c, err := client.NewConfig("ops", pki.RoleAdmin, l.ta.secrets.OSCA, l.ta.secrets.OSCABundle(), time.Hour, time.Now())
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
// upgrade may change a node's variant, and the node checks the config it gets against the image.
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
	c, err := loadInventory(context.Background(), l.inventory)
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
		facts, err := kubernetes.FactsPayload([]byte(testImageJSON))
		if err != nil {
			return nil, err
		}
		resp.LayerFacts = append(resp.LayerFacts, facts)
	}
	return connect.NewResponse(resp), nil
}

// applyConfig runs chalkctl apply-config on the lab's node with the args given.
func (l *upgradeLab) applyConfig(name string, args ...string) error {
	l.ta.stdout.Reset()
	l.ta.stderr.Reset()
	return l.ta.run(context.Background(), append([]string{"apply-config", name, "--inventory", l.inventory, "--address", l.addrs[name]}, args...))
}

//	TestApplyConfigStaged delivers the inventory's config for the staged image, checked against the
//
// image the inventory names for the node, and the running image's config warns of the staged one;
// status
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

// apply-config --staged checks the rules that compare the config with the node's against the
// running image's config, as the node does, while its diff compares with the staged image's.
func TestApplyConfigStagedChecksRulesAgainstTheRunningConfig(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w", "0.2.0")
	path := filepath.Join(img, "schemas", "kubernetes.chalkos.dev_v1alpha1_Node.json")
	var sch map[string]any
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &sch); err != nil {
		t.Fatal(err)
	}
	nodeType := sch["properties"].(map[string]any)["spec"].(map[string]any)["properties"].(map[string]any)["type"].(map[string]any)
	nodeType["x-chalkos-rules"] = []any{map[string]any{"rule": "self == oldSelf", "message": "the type is fixed"}}
	data, err := json.Marshal(sch)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))
	l.useInventory(t, map[string]string{"w": img})
	n := l.nodes["w1"]
	want := l.inventoryConfig("w1")
	n.staged, n.stagedRoot, n.stagedConfig = "0.2.0", rootOf(t, img), want
	n.config = strings.Replace(want, `"type":"worker"`, `"type":"controlplane"`, 1)
	err = l.applyConfig("w1", "--staged")
	if err == nil || !strings.Contains(err.Error(), `Node "default" spec.type: the type is fixed`) {
		t.Fatalf("err = %v", err)
	}
	if l.ta.stdout.String() != "w1: config unchanged\n" {
		t.Errorf("stdout = %q, want the staged config's diff", l.ta.stdout)
	}
}

// GetConfig returns the config the node runs, or with staged the staged image's, redacted as
// chalkd redacts it.
func (n *upgradeFake) GetConfig(ctx context.Context, req *connect.Request[nodev1.GetConfigRequest]) (*connect.Response[nodev1.GetConfigResponse], error) {
	n.lab.mu.Lock()
	denied, failure := n.lab.configDenied, n.lab.configErrs[n.name]
	if n.lab.configAsked == nil {
		n.lab.configAsked = map[string]int{}
	}
	n.lab.configAsked[n.name]++
	n.lab.mu.Unlock()
	if denied {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("GetConfig needs the admin role"))
	}
	if failure != nil {
		return nil, failure
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
	if want := "w1: config 4 added, 0 removed, 0 changed\n"; !strings.Contains(l.ta.stdout.String(), want) {
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
	if !strings.Contains(out, "w1: config unchanged\n") || !strings.Contains(out, "w2: config 4 added, 0 removed, 0 changed\n+ Cluster \"default\"\n") {
		t.Errorf("output:\n%s", out)
	}
	l.ta.stdout.Reset()
	if err := l.upgrade(img, "--nodes", "w1", "--no-reboot"); err != nil {
		t.Fatal(err)
	}
	// The image renders its outputs at its first boot, which the run says.
	if out := l.ta.stdout.String(); !strings.Contains(out, "w1: config unchanged\nw1: installed ") || !strings.Contains(out, " renders and checks any outputs it declares when it first boots; if one fails, the node falls back to the image it runs\n") {
		t.Errorf("output:\n%s", out)
	}
}

// TestUpgradeRollsBack boots the nodes' previous image again through the upgrade's steps:
// drained, the image staged, rebooted, waited for and uncordoned. Run again, it finds every node
// on the image and changes nothing; an upgrade to the inventory's image undoes the rollback, after
// which a rollback works again.
func TestUpgradeRollsBack(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "w", "0.2.0")
	if err := l.upgrade(img, "--nodes", "w1,w2"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	l.takeEvents()
	l.ta.stdout.Reset()
	if err := l.run("upgrade", "--rollback", "--nodes", "w1,w2"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	want := []string{"w1 drain", "w1 rollback", "w1 reboot", "w1 uncordon", "w2 drain", "w2 rollback", "w2 reboot", "w2 uncordon"}
	if got := l.takeEvents(); !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	for _, w := range []string{"w1", "w2"} {
		if v := l.images()[w]; v != "0.1.0" {
			t.Errorf("%s runs %s, want 0.1.0", w, v)
		}
	}
	for _, line := range []string{"rolling back 2 nodes to 010101010101: w1, w2", "w1: rolling back to 010101010101",
		"w1: 010101010101 boots next as chalkos_3+3.efi", "w1: runs 010101010101, found healthy", "rolled back 2 nodes to 010101010101"} {
		if !strings.Contains(l.ta.stdout.String(), line) {
			t.Errorf("output lacks %q:\n%s", line, l.ta.stdout)
		}
	}
	// Run again: the nodes run the image a rollback got them to, so nothing changes.
	if err := l.run("upgrade", "--rollback", "--nodes", "w1,w2"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("a second run asked %q", got)
	}
	if v := l.images()["w1"]; v != "0.1.0" {
		t.Errorf("after a second run w1 runs %s, want 0.1.0", v)
	}
	// An upgrade undoes the rollback; a rollback then boots the image before it again.
	if err := l.upgrade(img, "--nodes", "w1,w2"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if err := l.run("upgrade", "--rollback", "--nodes", "w1,w2"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if v := l.images()["w2"]; v != "0.1.0" {
		t.Errorf("after the upgrade and another rollback w2 runs %s, want 0.1.0", v)
	}
}

// TestUpgradeRollbackContinues runs again after a run stopped at a node: the node a rollback got
// to the image is left alone, and the run goes on with the others to the same image instead of
// booting that node's own previous image.
func TestUpgradeRollbackContinues(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.upgrade(testUpgradeImage(t, "w", "0.2.0"), "--nodes", "w1,w2"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	l.refuseRollback = "w2"
	if err := l.run("upgrade", "--rollback", "--nodes", "w1,w2"); err == nil || !strings.HasPrefix(err.Error(), "w2: ") {
		t.Fatalf("rollback = %v, want w2's refusal", err)
	}
	l.takeEvents()
	if err := l.run("upgrade", "--rollback", "--nodes", "w1,w2"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got, want := l.takeEvents(), []string{"w2 drain", "w2 rollback", "w2 reboot", "w2 uncordon"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	for _, w := range []string{"w1", "w2"} {
		if v := l.images()[w]; v != "0.1.0" {
			t.Errorf("%s runs %s, want 0.1.0", w, v)
		}
	}
}

// TestUpgradeRollbackSkipsNodesOnTheTarget rolls back every node of the cluster when only one was
// upgraded: the others run the image it rolls back to, and are left alone. The node stages the
// image without boot counting, which the run says, also once the node runs it.
func TestUpgradeRollbackSkipsNodesOnTheTarget(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.upgrade(testUpgradeImage(t, "w", "0.2.0"), "--nodes", "w1"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	l.takeEvents()
	l.uncountedRollback = true
	l.ta.stdout.Reset()
	if err := l.run("upgrade", "--rollback"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got, want := l.takeEvents(), []string{"w1 drain", "w1 rollback", "w1 reboot", "w1 uncordon"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	for _, line := range []string{"w1: 010101010101 boots next as chalkos_3.efi, without boot counting", "w1: runs 010101010101 (its boots are not counted)"} {
		if !strings.Contains(l.ta.stdout.String(), line) {
			t.Errorf("output lacks %q:\n%s", line, l.ta.stdout)
		}
	}
	if line := "w1: runs 010101010101, found healthy"; strings.Contains(l.ta.stdout.String(), line) {
		t.Errorf("output says %q of a boot that is not counted:\n%s", line, l.ta.stdout)
	}
}

// TestUpgradeRollbackRefuses stops before any node changes when no node keeps a previous image,
// when the nodes would roll back to different images, and when a node keeps none and does not run
// the others' target; it refuses flags that install an image or deliver config.
func TestUpgradeRollbackRefuses(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.run("upgrade", "--rollback"); err == nil || !strings.Contains(err.Error(), "no node keeps a previous image to roll back to") {
		t.Errorf("without previous images: %v", err)
	}
	if err := l.upgrade(testUpgradeImage(t, "w", "0.2.0"), "--nodes", "w1,w2"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if err := l.upgrade(testUpgradeImage(t, "w", "0.3.0"), "--nodes", "w1"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	l.takeEvents()
	err := l.run("upgrade", "--rollback", "--nodes", "w1,w2")
	if err == nil || !strings.Contains(err.Error(), "the nodes roll back to different images: ") || !strings.Contains(err.Error(), "roll them back separately with --nodes") {
		t.Errorf("different previous images: %v", err)
	}
	l.nodes["n1"].root = bytes.Repeat([]byte{9}, 32)
	if err := l.run("upgrade", "--rollback", "--nodes", "w2,n1"); err == nil || !strings.Contains(err.Error(), "n1 does not run 010101010101, which the other nodes roll back to, and keeps no previous image") {
		t.Errorf("a node off the target without a previous image: %v", err)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("refused runs asked %q", got)
	}
	img := testUpgradeImage(t, "w", "0.4.0")
	for _, args := range [][]string{{"--image", img}, {"--diff"}, {"--retry-failed"}} {
		if err := l.run(append([]string{"upgrade", "--rollback"}, args...)...); err == nil || !strings.Contains(err.Error(), "--rollback") {
			t.Errorf("--rollback %s = %v, want a usage error", args[0], err)
		}
	}
}

// TestUpgradeRollbackWithoutAReboot stages the previous image without draining or rebooting the
// node and says that it boots with the next reboot; run again without --no-reboot, it finds the
// image staged and reboots the node into it.
func TestUpgradeRollbackWithoutAReboot(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.upgrade(testUpgradeImage(t, "w", "0.2.0"), "--nodes", "w1"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	l.takeEvents()
	l.ta.stdout.Reset()
	if err := l.run("upgrade", "--rollback", "--nodes", "w1", "--no-reboot"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got, want := l.takeEvents(), []string{"w1 rollback"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	if line := "staged 010101010101 on 1 node; it boots with their next reboot"; !strings.Contains(l.ta.stdout.String(), line) {
		t.Errorf("output lacks %q:\n%s", line, l.ta.stdout)
	}
	l.ta.stdout.Reset()
	if err := l.run("upgrade", "--rollback", "--nodes", "w1"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got, want := l.takeEvents(), []string{"w1 drain", "w1 reboot", "w1 uncordon"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	if line := "w1: 010101010101 is staged already"; !strings.Contains(l.ta.stdout.String(), line) {
		t.Errorf("output lacks %q:\n%s", line, l.ta.stdout)
	}
	if v := l.images()["w1"]; v != "0.1.0" {
		t.Errorf("w1 runs %s, want 0.1.0", v)
	}
}

// TestUpgradeRollbackWithoutAnInventory rolls back the nodes --address names, as a partial
// cluster: the rollback needs no inventory, as it installs no image and delivers no config.
func TestUpgradeRollbackWithoutAnInventory(t *testing.T) {
	l := newUpgradeLab(t, 1)
	if err := l.upgrade(testUpgradeImage(t, "w", "0.2.0"), "--nodes", "w1"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	l.takeEvents()
	l.ta.stdout.Reset()
	args := []string{"upgrade", "--rollback"}
	for _, name := range []string{"cp1", "w1"} {
		args = append(args, "--address", name+"="+l.addrs[name])
	}
	if err := l.ta.run(context.Background(), args); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if v := l.images()["w1"]; v != "0.1.0" {
		t.Errorf("w1 runs %s, want 0.1.0", v)
	}
	if got := l.takeEvents(); !slices.Contains(got, "w1 rollback") || !slices.Contains(got, "w1 reboot") {
		t.Errorf("events %q, want w1 rolled back and rebooted", got)
	}
	if line := "rolled back 2 nodes to 010101010101"; !strings.Contains(l.ta.stdout.String(), line) {
		t.Errorf("output lacks %q:\n%s", line, l.ta.stdout)
	}
}

// TestUpgradeRollbackFallsBack stops at a node whose rollback image falls back after its tries,
// with the image it fell back to and the node left cordoned.
func TestUpgradeRollbackFallsBack(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "w", "0.2.0")
	if err := l.upgrade(img, "--nodes", "w1"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	l.takeEvents()
	l.unhealthy["0.1.0"] = true
	err := l.run("upgrade", "--rollback", "--nodes", "w1")
	if err == nil || !strings.Contains(err.Error(), "w1: rollback to 010101010101 failed: it fell back to "+shortOf(t, img)+"; it stays cordoned") {
		t.Fatalf("rollback = %v", err)
	}
	if got, want := l.takeEvents(), []string{"w1 drain", "w1 rollback", "w1 reboot"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	if !l.cordoned["w1"] {
		t.Error("w1 was uncordoned")
	}
}

// TestUpgradeRollbackOfAControlPlane rolls back a control plane as an upgrade takes it down: only
// while the others keep etcd's quorum, drained before it reboots and uncordoned once back.
func TestUpgradeRollbackOfAControlPlane(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.upgrade(testUpgradeImage(t, "cp", "0.2.0"), "--nodes", "cp1"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	l.takeEvents()
	l.unhealthyMembers = []string{"cp2"}
	if err := l.run("upgrade", "--rollback", "--nodes", "cp1", "--timeout", "100ms"); err == nil || !strings.Contains(err.Error(), "without cp1 etcd has 1 healthy voter of 3") {
		t.Errorf("rollback without quorum = %v", err)
	}
	for _, e := range l.takeEvents() {
		if !strings.HasSuffix(e, " members") {
			t.Errorf("the refused run asked %q", e)
		}
	}
	l.unhealthyMembers = nil
	if err := l.run("upgrade", "--rollback", "--nodes", "cp1"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got, want := l.takeEvents(), []string{"cp1 members", "cp1 rollback", "cp1 drain", "cp1 reboot", "cp1 uncordon"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	if v := l.images()["cp1"]; v != "0.1.0" {
		t.Errorf("cp1 runs %s, want 0.1.0", v)
	}
}
