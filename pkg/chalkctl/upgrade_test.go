package chalkctl

import (
	"bytes"
	"cmp"
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
	"google.golang.org/protobuf/types/known/timestamppb"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/api/kubernetes/v1/kubernetesv1connect"
	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/image/imagetest"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/internal/poll"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/ociimage/ocitest"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/schema/schematest"
	"github.com/trevex/chalkos/pkg/statusview"
)

// testUpgradeImage builds a metal disk image of the name as chalkos images are laid out: an
// ESP holding systemd-boot and the UKI, the store's hash tree and the store, described by
// repart-output.json. The label seeds the store, so images of different labels or names differ
// in their root hashes, and the fake nodes say which image they run by it.
func testUpgradeImage(t *testing.T, imageName, label string) string {
	t.Helper()
	return testImageOf(t, imageName, "metal", label, 512, nil)
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
	return statusview.ImageName(rootOf(t, dir), "", "")
}

// testImageOf builds a test image whose UKI's os-release edit changes, when given, from what
// chalkos's images name, with an empty IMAGE_VERSION.
func testImageOf(t *testing.T, imageName, platform, label string, blockSize int, edit func(osRelease map[string]string)) string {
	t.Helper()
	imgDir := filepath.Join(t.TempDir(), "image")
	root := imagetest.Write(t, imgDir, imagetest.Image{Name: imageName, Platform: platform, Seed: label, BlockSize: blockSize, Edit: edit})
	testImageNames.Store(string(root), label)
	// The contract chalkctl checks a node's config against, and the metadata it reads the image's
	// root hash from: the upgrade lab's images cp-*, w-* and k8s-* run Kubernetes.
	copySchemas(t, filepath.Join(imgDir, "schemas"))
	if runsKubernetes(imageName) {
		writeFile(t, filepath.Join(imgDir, "facts", "kubernetes.json"), testImageJSON)
	}
	writeFile(t, filepath.Join(imgDir, "metadata.json"), fmt.Sprintf(`{"schemaVersion": 0, "id": "chalkos", "name": %q, "platform": %q, "rootHash": %q, "kinds": []}`,
		imageName, platform, hex.EncodeToString(root)))
	return imgDir
}

// upgradeFake answers chalkctl upgrade as chalkd does, keeping in memory what the node's disk and
// the cluster would: the image it runs, one installed beside it, one it fell back from, each by
// its test name and root hash, and whether its Node is cordoned.
type upgradeFake struct {
	nodev1connect.UnimplementedNodeServiceHandler
	lab                       *upgradeLab
	name, imageName, platform string
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
	// active is set while the staged image boots next; an image staged alone waits for
	// ActivateImage and a reboot boots the running image again.
	active bool
	// boots counts the node's reboots, which name its boot's ID.
	boots int
	// config and stagedConfig are the config the node runs and the one kept for the staged image, as
	// delivered.
	config, stagedConfig string
	// pending is the config the node holds pending for its next upgrade, as config apply --staged
	// keeps it without a staged image; empty without one.
	pending string
	// secret is the fallback secret the last upgrade sent.
	secret string
	// stagedStoreSize and stagedVeritySize are the bytes of store and hash tree staged.
	stagedStoreSize, stagedVeritySize uint64
	// rebooting makes the node unreachable, as while it reboots.
	rebooting bool
	// alreadyStaged makes the node answer an upgrade as one that found the image staged already,
	// though its status did not say so, as when another run staged it in between.
	alreadyStaged bool
	// keepPending makes the node keep its pending config through an upgrade, as one that stages the
	// known-good image a rollback left with the config it last ran.
	keepPending bool
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
	// trial is the config on trial the node's status shows; nil without one. trialPrevious is the
	// config a rollback of it restores.
	trial         *nodev1.ConfigTrial
	trialPrevious string
	// apiServer is the API server the node runs while its config names none, and kubelet its image's
	// kubelet; 1.37.1 when empty.
	apiServer, kubelet string
}

// upgradeLab is a cluster of fake nodes.
type upgradeLab struct {
	t  *testing.T
	ta *testApp
	// schemas are the fixture schemas the nodes' contracts hold.
	schemas map[string][]byte
	nodes   map[string]*upgradeFake
	addrs   map[string]string
	// inventory is the lab's inventory once useInventory wrote it.
	inventory string

	mu sync.Mutex
	// events records what the nodes were asked, in order.
	events []string
	// unhealthy images never become healthy, so their nodes fall back.
	unhealthy map[string]bool
	// sick holds why the boots of an image are not healthy while the nodes run it, as a node's
	// check of its boot finds it; the boot loader counts none of them, so none falls back.
	sick map[string]string
	// settling makes the nodes report their boots as still starting, so a boot that is not
	// healthy may still become so.
	settling bool
	// flaky is the number of checks of a boot's health, of any node, that find it settled and
	// unhealthy before the lab answers as it would, as when etcd answers late.
	flaky int
	// noBootID makes the nodes report no boot ID, as a chalkd that cannot read it.
	noBootID bool
	// healthChecked holds the IDs of the boots whose health each node was asked to check, in order.
	healthChecked map[string][]string
	// unhealthyMembers are etcd members that do not answer.
	unhealthyMembers []string
	// cordoned holds the cordoned Nodes, and whether an upgrade marked them so.
	cordoned map[string]bool
	// refuseUpgrade makes the node refuse its next upgrade, as if the transfer broke.
	refuseUpgrade string
	// refuseActivate makes the node refuse its next activation, as if Secure Boot would refuse the
	// staged UKI.
	refuseActivate string
	// activateLost makes the node fail its next activation after it made the image boot next, as
	// when syncing the ESP after the rename fails.
	activateLost string
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
	// stuck holds the control planes whose static pods never run the images their config names.
	stuck map[string]bool
	// refuseConfig makes the node refuse its next delivery of config.
	refuseConfig string
	// dropAfterDelivery makes the node unreachable for that many calls after its next delivery of
	// config, as a config that restarts its network does.
	dropAfterDelivery map[string]int
	// unreachable counts the calls the node does not answer yet.
	unreachable map[string]int
	// hangAfterDelivery makes the node's Kubernetes status hang, once it was delivered config,
	// until the caller gives up, as a layer's service stuck on an API server that does not answer.
	hangAfterDelivery string
	// hanging is the node whose Kubernetes status hangs.
	hanging string
	// kubernetesFails holds the nodes whose Kubernetes status fails, as while their API server does
	// not answer.
	kubernetesFails map[string]bool
	// uncordonAfter holds the uncordon of a node until the node it names was drained, a minute at
	// most, so the nodes of a batch upgrade together however late one of them starts.
	uncordonAfter map[string]string
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

// awaitDrained waits a minute at most until the node uncordonAfter names for node was drained,
// and logs a wait that ran out, as when the nodes of a batch did not upgrade together.
func (l *upgradeLab) awaitDrained(node string) {
	if poll.Until(context.Background(), "", time.Minute, 5*time.Millisecond, func() error {
		l.mu.Lock()
		defer l.mu.Unlock()
		if other := l.uncordonAfter[node]; other != "" && !slices.Contains(l.events, other+" drain") {
			return errors.New("not drained")
		}
		return nil
	}) == nil {
		return
	}
	l.mu.Lock()
	other := l.uncordonAfter[node]
	l.mu.Unlock()
	l.t.Logf("uncordoned %s after waiting a minute for %s to be drained, which upgrades in the same batch", node, other)
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
	rebooting := n.rebooting
	n.mu.Unlock()
	if rebooting {
		return connect.NewError(connect.CodeUnavailable, errors.New("the node reboots"))
	}
	n.lab.mu.Lock()
	defer n.lab.mu.Unlock()
	if n.lab.unreachable[n.name] > 0 {
		n.lab.unreachable[n.name]--
		return connect.NewError(connect.CodeUnavailable, errors.New("the node restarts its network"))
	}
	return nil
}

func (n *upgradeFake) Info(context.Context, *connect.Request[nodev1.InfoRequest]) (*connect.Response[nodev1.InfoResponse], error) {
	if err := n.down(); err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return connect.NewResponse(&nodev1.InfoResponse{RootHash: n.root, ImageId: "chalkos", Image: n.imageName, Platform: n.platform, Layers: n.layers}), nil
}

func (n *upgradeFake) Status(_ context.Context, req *connect.Request[nodev1.StatusRequest]) (*connect.Response[nodev1.StatusResponse], error) {
	if err := n.down(); err != nil {
		return nil, err
	}
	n.lab.mu.Lock()
	sick, settling, noBootID := maps.Clone(n.lab.sick), n.lab.settling, n.lab.noBootID
	flaky := req.Msg.GetCheckHealth() && n.lab.flaky > 0
	if flaky {
		n.lab.flaky--
	}
	n.lab.mu.Unlock()
	// The boot IDs of the looks that checked health are recorded once the node is unlocked.
	var checked string
	defer func() {
		if req.Msg.GetCheckHealth() {
			n.lab.mu.Lock()
			n.lab.healthChecked[n.name] = append(n.lab.healthChecked[n.name], checked)
			n.lab.mu.Unlock()
		}
	}()
	n.mu.Lock()
	defer n.mu.Unlock()
	st := &nodev1.StatusResponse{Boot: &nodev1.BootStatus{
		Running: &nodev1.BootImage{RootHash: n.root, Generation: n.generation},
		Entry:   fmt.Sprintf("chalkos_%d.efi", n.generation), Blessed: true,
		BootId: fmt.Sprintf("boot-%d", n.boots),
	}}
	if noBootID {
		st.Boot.BootId = ""
	}
	if req.Msg.GetCheckHealth() {
		st.Boot.Health = &nodev1.BootHealth{Healthy: sick[n.image] == "", Error: sick[n.image], Settled: !settling}
		if flaky {
			st.Boot.Health = &nodev1.BootHealth{Error: "etcd: context deadline exceeded", Settled: true}
		}
		checked = fmt.Sprintf("boot-%d", n.boots)
	}
	// VAR, which every node has.
	st.Volumes = []*nodev1.VolumeStatus{{Name: "var"}}
	if n.staged != "" {
		st.Boot.Staged = &nodev1.BootImage{RootHash: n.stagedRoot, Generation: n.generation + 1}
		st.Boot.StagedState = nodev1.SlotState_SLOT_STATE_STAGED
		if n.active {
			st.Boot.StagedState = nodev1.SlotState_SLOT_STATE_ACTIVE
		}
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
	st.Trial = n.trial
	if n.pending != "" {
		st.PendingConfig = &nodev1.PendingConfig{Version: versionOf(n.pending), Since: timestamppb.New(pendingSince)}
	}
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

func (n upgradeKubernetes) Status(ctx context.Context, _ *connect.Request[kubernetesv1.StatusRequest]) (*connect.Response[kubernetesv1.StatusResponse], error) {
	if err := n.down(); err != nil {
		return nil, err
	}
	n.lab.mu.Lock()
	if n.lab.hanging == n.name {
		n.lab.mu.Unlock()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if n.lab.kubernetesFails[n.name] {
		n.lab.mu.Unlock()
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("the API server does not answer"))
	}
	ready := "True"
	if n.lab.notReady[n.name] {
		ready = "False"
	}
	n.lab.mu.Unlock()
	switch n.kind {
	case kubernetes.KindControlPlane:
		pods := n.controlPlanePods()
		return connect.NewResponse(&kubernetesv1.StatusResponse{Kind: n.kind, State: "bootstrapped", Phase: kubernetesv1.Phase_PHASE_BOOTSTRAPPED, NodeReady: ready, ApiServerVersion: n.apiServerVersion(), KubeletVersion: n.kubeletVersion(), ControlPlanePods: pods, ControlPlanePodsCurrent: pods == "current"}), nil
	case kubernetes.KindWorker:
		return connect.NewResponse(&kubernetesv1.StatusResponse{Kind: n.kind, State: "joined", Phase: kubernetesv1.Phase_PHASE_JOINED, NodeReady: ready, ApiServerVersion: n.apiServerVersion(), KubeletVersion: n.kubeletVersion()}), nil
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
		return connect.NewResponse(&nodev1.UpgradeResponse{AlreadyInstalled: true, Pending: n.pendingOutcome(nodev1.PendingFate_PENDING_FATE_KEPT)}), nil
	}
	name, _ := testImageNames.Load(string(h.RootHash))
	// An image staged again stays active; another one is staged anew, and activated unless the
	// upgrade stops at staging.
	n.active = n.active && bytes.Equal(n.stagedRoot, h.RootHash) || !header.GetStageOnly()
	n.staged, n.stagedRoot, n.failed, n.failedRoot = name.(string), h.RootHash, "", nil
	n.previous, n.previousRoot, n.previousConfig, n.rolledBackTo = "", nil, "", nil
	// The image gets the pending config unless the upgrade carries config; either way the node
	// drops it, and says so.
	n.stagedConfig, n.secret = cmp.Or(header.GetConfig(), n.pending), header.GetFallbackSecret()
	fate := nodev1.PendingFate_PENDING_FATE_TAKEN
	switch {
	case n.keepPending:
		fate = nodev1.PendingFate_PENDING_FATE_KEPT
	case header.GetConfig() != "":
		fate = nodev1.PendingFate_PENDING_FATE_REPLACED
	}
	pending := n.pendingOutcome(fate)
	if !n.keepPending {
		n.pending = ""
	}
	n.stagedStoreSize, n.stagedVeritySize = h.Store.Size, h.HashTree.Size
	n.header = header
	resp := &nodev1.UpgradeResponse{AlreadyStaged: n.alreadyStaged, Entry: n.entry(), Active: n.active, Pending: pending}
	return connect.NewResponse(resp), nil
}

// entry is the file name of the staged image's UKI: its staged name, or its entry's while it is
// active.
func (n *upgradeFake) entry() string {
	if n.active {
		return fmt.Sprintf("chalkos_%d+3.efi", n.generation+1)
	}
	return fmt.Sprintf(".staged-chalkos_%d.efi", n.generation+1)
}

// ActivateImage makes the staged image boot next, as chalkd renames its UKI.
func (n *upgradeFake) ActivateImage(_ context.Context, req *connect.Request[nodev1.ActivateImageRequest]) (*connect.Response[nodev1.ActivateImageResponse], error) {
	if err := n.down(); err != nil {
		return nil, err
	}
	n.lab.record("%s activate", n.name)
	n.lab.mu.Lock()
	refuse := n.lab.refuseActivate == n.name
	if refuse {
		n.lab.refuseActivate = ""
	}
	lost := n.lab.activateLost == n.name
	if lost {
		n.lab.activateLost = ""
	}
	n.lab.mu.Unlock()
	n.mu.Lock()
	defer n.mu.Unlock()
	switch {
	case refuse:
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("activate: Secure Boot would refuse the UKI"))
	case bytes.Equal(req.Msg.RootHash, n.root):
		return connect.NewResponse(&nodev1.ActivateImageResponse{AlreadyInstalled: true}), nil
	case n.staged == "":
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("activate: no image is staged in the inactive slot"))
	case !bytes.Equal(req.Msg.RootHash, n.stagedRoot):
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("activate: the image staged in the inactive slot is %x, not %x", n.stagedRoot, req.Msg.RootHash))
	case n.active:
		return connect.NewResponse(&nodev1.ActivateImageResponse{AlreadyActive: true, Entry: n.entry()}), nil
	}
	n.active = true
	if lost {
		return nil, connect.NewError(connect.CodeInternal, errors.New("activate: sync the ESP: input/output error"))
	}
	return connect.NewResponse(&nodev1.ActivateImageResponse{Entry: n.entry()}), nil
}

// pendingOutcome is the answer of an upgrade that left the pending config to fate; nil without one.
func (n *upgradeFake) pendingOutcome(fate nodev1.PendingFate) *nodev1.PendingOutcome {
	if n.pending == "" {
		return nil
	}
	return &nodev1.PendingOutcome{Version: versionOf(n.pending), Fate: fate}
}

// Reboot boots the staged image while it is active, which falls back after its tries when the lab
// makes it unhealthy; a reboot of a node whose image is staged alone boots the running image again.
// The node answers as before for a moment, as chalkd does until the reboot stops it.
func (n *upgradeFake) Reboot(context.Context, *connect.Request[nodev1.RebootRequest]) (*connect.Response[nodev1.RebootResponse], error) {
	if err := n.down(); err != nil {
		return nil, err
	}
	n.lab.record("%s reboot", n.name)
	go func() {
		time.Sleep(15 * time.Millisecond)
		n.mu.Lock()
		n.rebooting = true
		n.mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		n.lab.mu.Lock()
		unhealthy := n.lab.unhealthy[n.staged]
		n.lab.mu.Unlock()
		n.mu.Lock()
		defer n.mu.Unlock()
		n.rebooting = false
		n.boots++
		switch {
		case n.staged == "" || !n.active:
		case unhealthy:
			n.failed, n.failedRoot, n.staged, n.stagedRoot, n.active = n.staged, n.stagedRoot, "", nil, false
		default:
			n.previous, n.previousRoot, n.previousConfig = n.image, n.root, n.config
			n.image, n.root, n.staged, n.stagedRoot, n.active = n.staged, n.stagedRoot, "", nil, false
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
	n.staged, n.stagedRoot, n.stagedConfig, n.active = n.previous, n.previousRoot, n.previousConfig, true
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
	n.lab.awaitDrained(req.Msg.Node)
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
	return newUpgradeLabOf(t, controlPlanes, []string{"w1", "w2", "w3"}, "cp-metal", "w-metal")
}

// newUpgradeLabOf runs a fake chalkd for each of the control planes cp1 and on, running
// the image cpImage, the workers named, running wImage, which may be the same, and n1, a node
// running plain-metal without Kubernetes, and describes them in an inventory.
func newUpgradeLabOf(t *testing.T, controlPlanes int, workers []string, cpImage, wImage string) *upgradeLab {
	t.Helper()
	ta := newTestApp(t)
	ta.pollInterval = 5 * time.Millisecond
	ta.unhealthyGrace = 200 * time.Millisecond
	var nodes []testNode
	for i := 1; i <= controlPlanes; i++ {
		nodes = append(nodes, testNode{fmt.Sprintf("cp%d", i), kubernetes.KindControlPlane, cpImage})
	}
	for _, w := range workers {
		nodes = append(nodes, testNode{w, kubernetes.KindWorker, wImage})
	}
	nodes = append(nodes, testNode{"n1", "", "plain-metal"})
	l := &upgradeLab{t: t, ta: ta, schemas: schematest.Files(t, fixtureSchemas...), nodes: map[string]*upgradeFake{}, addrs: map[string]string{}, unhealthy: map[string]bool{}, sick: map[string]string{}, cordoned: map[string]bool{}, emptyDir: map[string]bool{}, stopping: map[string]bool{}, notReady: map[string]bool{}, healthChecked: map[string][]string{}}
	for _, node := range nodes {
		name := node.name
		n := &upgradeFake{lab: l, name: name, imageName: node.image, platform: "metal", kind: node.kind, image: "0.1.0", root: bytes.Repeat([]byte{1}, 32), generation: 1}
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
// each node runs its image on metal, whose directory images gives by name.
func (l *upgradeLab) useInventory(t *testing.T, images map[string]string) {
	t.Helper()
	var nodes []testNode
	for _, name := range slices.Sorted(maps.Keys(l.nodes)) {
		n := l.nodes[name]
		nodes = append(nodes, testNode{name, n.kind, n.imageName})
	}
	l.inventory = writeNodes(t, l.ta.dir, nodes, images)
}

// run runs chalkctl with the lab's inventory and every node's address.
func (l *upgradeLab) run(args ...string) error {
	return l.ta.run(context.Background(), append(append(args, "--inventory", l.inventory), l.addressArgs()...))
}

// addressArgs are the --address flags of the nodes named, or of every node.
func (l *upgradeLab) addressArgs(names ...string) []string {
	if len(names) == 0 {
		names = slices.Collect(maps.Keys(l.addrs))
	}
	var args []string
	for _, name := range names {
		args = append(args, "--address", name+"="+l.addrs[name])
	}
	return args
}

// TestUpgradeGivesEachNodeItsImage upgrades each node to the image the inventory names for it.
func TestUpgradeGivesEachNodeItsImage(t *testing.T) {
	l := newUpgradeLab(t, 3)
	images := map[string]string{
		"cp-metal": testUpgradeImage(t, "cp-metal", "0.2.0"), "w-metal": testUpgradeImage(t, "w-metal", "0.2.0"), "plain-metal": testUpgradeImage(t, "plain-metal", "0.2.0"),
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
	cp, w, plain := shortOf(t, images["cp-metal"]), shortOf(t, images["w-metal"]), shortOf(t, images["plain-metal"])
	for _, line := range []string{"upgrading w-metal on metal to " + w + ": w1, w2, w3", "upgrading cp-metal on metal to " + cp + ": cp1, cp2, cp3",
		"upgraded 7 nodes to " + strings.Join(slices.Sorted(slices.Values([]string{cp, w, plain})), ", ")} {
		if !strings.Contains(l.ta.stdout.String(), line) {
			t.Errorf("output lacks %q:\n%s", line, l.ta.stdout)
		}
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

// TestUpgradeFromARegistry has the node pull the image by the digest chalkctl resolved from the
// tag.
func TestUpgradeFromARegistry(t *testing.T) {
	ctx := context.Background()
	dir := testUpgradeImage(t, "plain-metal", "0.2.0")
	writeFile(t, filepath.Join(dir, "repart.d", "00-esp.conf"), "[Partition]\nType=esp\n")
	img, closeImage, err := ociimage.FromDirectory(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	ref := ocitest.Registry(t) + "/lab/plain-metal:v2"
	pinned := ocitest.Push(t, img, ref)
	closeImage()

	l := newUpgradeLab(t, 1)
	l.useInventory(t, map[string]string{"plain-metal": "oci:" + ref})
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

// TestUpgradeControlPlanesInTurn upgrades one control plane at a time, each drained only while the
// others keep etcd's quorum.
func TestUpgradeControlPlanesInTurn(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "cp-metal", "0.2.0")
	if err := l.upgrade(img); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	var want []string
	for _, cp := range []string{"cp1", "cp2", "cp3"} {
		want = append(want, cp+" upgrade", cp+" members", cp+" drain", cp+" activate", cp+" reboot", cp+" uncordon")
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

// TestUpgradeOneImageControlPlanesFirst upgrades control planes before workers running the same
// image.
func TestUpgradeOneImageControlPlanesFirst(t *testing.T) {
	l := newUpgradeLabOf(t, 3, []string{"a1", "a2"}, "k8s-metal", "k8s-metal")
	if err := l.upgrade(testUpgradeImage(t, "k8s-metal", "0.2.0")); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	events := l.takeEvents()
	at := eventOrder(t, events)
	for _, w := range []string{"a1", "a2"} {
		if at(w+" drain") < at("cp3 uncordon") {
			t.Errorf("%s was drained before the control planes were upgraded: %q", w, events)
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

// TestUpgradeKeepsQuorum refuses, without --allow-downtime, a reboot that leaves etcd without its
// quorum, and leaves the image staged.
func TestUpgradeKeepsQuorum(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.unhealthyMembers = []string{"cp2"}
	err := l.upgrade(testUpgradeImage(t, "cp-metal", "0.2.0"), "--nodes", "cp1", "--timeout", "100ms")
	if err == nil || !strings.Contains(err.Error(), "without cp1 etcd has 1 healthy voter of 3, fewer than the 2 its quorum needs") {
		t.Errorf("upgrade = %v", err)
	}
	for _, e := range l.takeEvents() {
		if e != "cp1 upgrade" && !strings.HasSuffix(e, " members") {
			t.Errorf("the refused run asked %q", e)
		}
	}
	if n := l.nodes["cp1"]; n.staged != "0.2.0" || n.active {
		t.Errorf("cp1 has %q staged, active: %v; want it staged alone", n.staged, n.active)
	}

	single := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "cp-metal", "0.2.0")
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

// TestUpgradeWorkersInBatches upgrades at most --max-unavailable workers at once, a batch after the
// one before is back.
func TestUpgradeWorkersInBatches(t *testing.T) {
	l := newUpgradeLab(t, 3)
	// w1 and w2 form the first batch: each comes back only once the other was drained.
	l.uncordonAfter = map[string]string{"w1": "w2", "w2": "w1"}
	if err := l.upgrade(testUpgradeImage(t, "w-metal", "0.2.0"), "--max-unavailable", "2"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	events := l.takeEvents()
	at := eventOrder(t, events)
	for _, w := range []string{"w1", "w2", "w3"} {
		if !inOrder(at, w) {
			t.Errorf("%s out of order: %q", w, events)
		}
	}
	if at("w3 upgrade") < at("w1 uncordon") || at("w3 upgrade") < at("w2 uncordon") {
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
	img := testUpgradeImage(t, "w-metal", "0.3.0")
	err := l.upgrade(img)
	if err == nil || !strings.Contains(err.Error(), "w1: upgrade to "+shortOf(t, img)+" failed: rolled back to 010101010101; it stays cordoned") || !strings.Contains(err.Error(), "units failed: broken.service") {
		t.Fatalf("upgrade = %v", err)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"w1 upgrade", "w1 drain", "w1 activate", "w1 reboot"}) {
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

// TestUpgradeReleasesANodeWhoseActivationFails uncordons a drained node whose image fails to
// activate; a rerun reboots it.
func TestUpgradeReleasesANodeWhoseActivationFails(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "w-metal", "0.2.0")
	l.refuseActivate = "w2"
	if err := l.upgrade(img, "--nodes", "w2"); err == nil || !strings.HasPrefix(err.Error(), "w2: ") || !strings.Contains(err.Error(), "Secure Boot would refuse the UKI") {
		t.Fatalf("upgrade = %v", err)
	}
	if got, want := l.takeEvents(), []string{"w2 upgrade", "w2 drain", "w2 activate", "w2 uncordon"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	if n := l.nodes["w2"]; l.cordoned["w2"] || n.staged != "0.2.0" || n.active {
		t.Errorf("w2 cordoned: %v; staged %q, active: %v", l.cordoned["w2"], n.staged, n.active)
	}
	if err := l.upgrade(img, "--nodes", "w2"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got, want := l.takeEvents(), []string{"w2 drain", "w2 activate", "w2 reboot", "w2 uncordon"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
}

// TestUpgradeContinues resumes a stopped run where each node stopped and uncordons only the nodes
// it cordoned.
func TestUpgradeContinues(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "w-metal", "0.2.0")
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
	want := []string{"w2 upgrade", "w2 drain", "w2 activate", "w2 reboot", "w2 uncordon", "w3 drain", "w3 activate", "w3 reboot"}
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

// TestUpgradeWithoutReboot installs the image on every node running its name and makes it boot next
// with --until activate, and leaves the nodes as they run, without draining them.
func TestUpgradeWithoutReboot(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "cp-metal", "0.2.0")
	if err := l.upgrade(img, "--until", "activate"); err != nil {
		t.Fatal(err)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"cp1 upgrade", "cp1 activate", "cp2 upgrade", "cp2 activate", "cp3 upgrade", "cp3 activate"}) {
		t.Errorf("events %q", got)
	}
	if n := l.nodes["cp2"]; n.staged != "0.2.0" || !n.active || n.image != "0.1.0" {
		t.Errorf("cp2 runs %s with %s staged, active: %v", n.image, n.staged, n.active)
	}
	if want := "cp1: activated " + shortOf(t, img) + ", which boots next as chalkos_2+3.efi\n"; !strings.Contains(l.ta.stdout.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, l.ta.stdout)
	}
}

// TestUpgradeUntilStage stops with the image staged; --until activate and node reboot then take the
// nodes further.
func TestUpgradeUntilStage(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "w-metal", "0.2.0")
	if err := l.upgrade(img, "--until", "stage"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"w1 upgrade", "w2 upgrade", "w3 upgrade"}) {
		t.Errorf("events %q", got)
	}
	for _, w := range []string{"w1", "w2", "w3"} {
		if n := l.nodes[w]; n.staged != "0.2.0" || n.active || !n.header.GetStageOnly() {
			t.Errorf("%s has %s staged, active: %v, stage only: %v", w, n.staged, n.active, n.header.GetStageOnly())
		}
	}
	out := l.ta.stdout.String()
	if !strings.Contains(out, "w1: installed "+shortOf(t, img)+", staged as .staged-chalkos_2.efi\n") || !strings.Contains(out, "staged "+shortOf(t, img)+" on 3 nodes; chalkctl node activate makes it boot next\n") {
		t.Errorf("output:\n%s", out)
	}
	if err := l.upgrade(img, "--nodes", "w1", "--until", "activate"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"w1 activate"}) {
		t.Errorf("events %q, want w1's image activated without sending it again", got)
	}
	if err := l.run("node", "reboot", "w1"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"w1 drain", "w1 reboot", "w1 uncordon"}) {
		t.Errorf("events %q", got)
	}
	if v := l.images()["w1"]; v != "0.2.0" {
		t.Errorf("w1 runs %s", v)
	}
	for _, args := range [][]string{{"upgrade", "--until", "boot"}, {"upgrade", "--rollback", "--until", "stage"}} {
		if err := l.run(args...); !errors.Is(err, cli.ErrUsage) {
			t.Errorf("%q = %v, want a usage error", args, err)
		}
	}
}

// TestNodeActivate makes the node boot the image the inventory names for it next, which it must
// have staged.
func TestNodeActivate(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w-metal", "0.2.0")
	l.useInventory(t, map[string]string{"w-metal": img})
	activate := func(name string) error {
		return l.ta.run(context.Background(), []string{"node", "activate", name, "--inventory", l.inventory, "--address", l.addrs[name]})
	}
	if err := activate("w1"); err == nil || !strings.Contains(err.Error(), "no image is staged") {
		t.Errorf("activate without a staged image = %v", err)
	}
	if err := l.run("upgrade", "--nodes", "w1", "--until", "stage"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	l.takeEvents()
	l.ta.stdout.Reset()
	if err := activate("w1"); err != nil {
		t.Fatal(err)
	}
	if want := "w1: activated " + shortOf(t, img) + ", which boots next as chalkos_2+3.efi\nw1: chalkctl node reboot w1 boots it\n"; l.ta.stdout.String() != want {
		t.Errorf("output %q, want %q", l.ta.stdout, want)
	}
	if err := activate("w1"); err != nil || !strings.Contains(l.ta.stdout.String(), "w1: "+shortOf(t, img)+" boots next already, as chalkos_2+3.efi\n") {
		t.Errorf("activate again = %v:\n%s", err, l.ta.stdout)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"w1 activate", "w1 activate"}) || !l.nodes["w1"].active {
		t.Errorf("events %q, w1 active: %v", got, l.nodes["w1"].active)
	}
}

// TestNodeActivateImage activates the image --image names, as upgrade --image staged it, also
// without an inventory; without --image the command needs one, for the node's image.
func TestNodeActivateImage(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img, other := testUpgradeImage(t, "w-metal", "0.2.0"), testUpgradeImage(t, "w-metal", "0.3.0")
	activate := func(args ...string) error {
		return l.ta.run(context.Background(), append([]string{"node", "activate", "w1", "--address", l.addrs["w1"]}, args...))
	}
	if err := activate(); err == nil || !strings.Contains(err.Error(), "node activate") {
		t.Errorf("activate without an inventory or --image = %v, want it refused", err)
	}
	if err := l.upgrade(img, "--nodes", "w1", "--until", "stage"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if err := activate("--image", other); err == nil || !strings.Contains(err.Error(), "the image staged in the inactive slot is "+hex.EncodeToString(l.nodes["w1"].stagedRoot)) {
		t.Errorf("activate another image = %v", err)
	}
	l.takeEvents()
	l.ta.stdout.Reset()
	if err := activate("--image", img); err != nil {
		t.Fatal(err)
	}
	if want := "w1: activated " + shortOf(t, img) + ", which boots next as chalkos_2+3.efi\nw1: chalkctl node reboot w1 boots it\n"; l.ta.stdout.String() != want {
		t.Errorf("output %q, want %q", l.ta.stdout, want)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"w1 activate"}) || !l.nodes["w1"].active {
		t.Errorf("events %q, w1 active: %v", got, l.nodes["w1"].active)
	}
}

// TestNodeReboot reboots a node into the image it boots next, drained unless --no-drain, and stops
// at a fallback.
func TestNodeReboot(t *testing.T) {
	l := newUpgradeLab(t, 1)
	if err := l.run("node", "reboot", "w2", "--no-drain"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"w2 reboot"}) {
		t.Errorf("events %q", got)
	}
	if out := l.ta.stdout.String(); !strings.Contains(out, "w2: rebooting into 010101010101\n") || !strings.Contains(out, "w2: runs 010101010101, found healthy\n") {
		t.Errorf("output:\n%s", out)
	}
	// The node answered for the boot before as it rebooted; the command waited for the next.
	if n := l.nodes["w2"]; n.boots != 1 {
		t.Errorf("node reboot returned after %d boots of w2", n.boots)
	}
	// The entry stays blessed, but the node's check of the new boot decides. While the boot is
	// still starting, the command waits for it to become healthy, until its timeout.
	l.mu.Lock()
	l.sick["0.1.0"], l.settling = "units failed: broken.service", true
	l.mu.Unlock()
	err := l.run("node", "reboot", "w2", "--no-drain", "--timeout", "1s")
	if err == nil || !strings.Contains(err.Error(), "w2: waiting for 010101010101 to be found healthy: its boot is not healthy yet: units failed: broken.service") {
		t.Errorf("node reboot into a boot that is not healthy yet = %v", err)
	}
	// Once the boot settled and stayed unhealthy for the grace period, its result is final: the
	// command stops long before its timeout, and the node it drained stays cordoned.
	l.mu.Lock()
	l.settling = false
	l.mu.Unlock()
	l.takeEvents()
	start := time.Now()
	err = l.run("node", "reboot", "w2", "--timeout", "1m")
	if err == nil || err.Error() != "w2: its boot of 010101010101 settled and is not healthy: units failed: broken.service; it stays cordoned" {
		t.Errorf("node reboot into a settled boot that is not healthy = %v", err)
	}
	if waited := time.Since(start); waited < 200*time.Millisecond || waited > 30*time.Second {
		t.Errorf("the command waited %v for a settled boot, want the grace period of 200ms and less than the timeout", waited)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"w2 drain", "w2 reboot"}) || !l.cordoned["w2"] {
		t.Errorf("events %q, w2 cordoned: %v", got, l.cordoned["w2"])
	}
	if n := l.nodes["w2"]; n.boots != 3 {
		t.Errorf("w2 booted %d times, want 3", n.boots)
	}
	l.mu.Lock()
	delete(l.sick, "0.1.0")
	l.mu.Unlock()
	l.unhealthy["0.3.0"] = true
	img := testUpgradeImage(t, "w-metal", "0.3.0")
	if err := l.upgrade(img, "--nodes", "w3", "--until", "activate"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	l.takeEvents()
	err = l.run("node", "reboot", "w3")
	if err == nil || !strings.Contains(err.Error(), "w3: its boot of "+shortOf(t, img)+" failed: it fell back to 010101010101; it stays cordoned") {
		t.Errorf("node reboot into an image that falls back = %v", err)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"w3 drain", "w3 reboot"}) || !l.cordoned["w3"] {
		t.Errorf("events %q, w3 cordoned: %v", got, l.cordoned["w3"])
	}
}

// TestNodeRebootWaitsOutAnUnhealthyCheck waits out an unhealthy boot shorter than the grace period.
func TestNodeRebootWaitsOutAnUnhealthyCheck(t *testing.T) {
	l := newUpgradeLab(t, 1)
	l.mu.Lock()
	l.flaky = 3
	l.mu.Unlock()
	if err := l.run("node", "reboot", "w2", "--no-drain", "--timeout", "1m"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if !strings.Contains(l.ta.stdout.String(), "w2: runs 010101010101, found healthy\n") {
		t.Errorf("output:\n%s", l.ta.stdout)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.flaky != 0 {
		t.Errorf("%d of the unhealthy checks were not asked for", l.flaky)
	}
	if checked := l.healthChecked["w2"]; len(checked) < 4 || slices.ContainsFunc(checked, func(id string) bool { return id != "boot-1" }) {
		t.Errorf("the node checked the health of the boots %q, want boot-1 alone, at least 4 times", checked)
	}
}

// TestNodeRebootWithoutTheBootBefore waits until the timeout when the node reports no boot ID,
// since the old boot may still answer.
func TestNodeRebootWithoutTheBootBefore(t *testing.T) {
	l := newUpgradeLab(t, 1)
	l.mu.Lock()
	l.noBootID, l.sick["0.1.0"] = true, "units failed: broken.service"
	l.mu.Unlock()
	start := time.Now()
	err := l.run("node", "reboot", "w2", "--no-drain", "--timeout", "1s")
	if err == nil || !strings.Contains(err.Error(), "w2: waiting for 010101010101 to be found healthy: its boot settled and is not healthy: units failed: broken.service") {
		t.Errorf("node reboot without the boot before = %v", err)
	}
	if waited := time.Since(start); waited < time.Second {
		t.Errorf("the command stopped after %v, before its timeout", waited)
	}
}

// TestUpgradeHoldsANodeWhoseImageBootsNextAfterAFailedActivation keeps a node cordoned whose failed
// activation left the image booting next.
func TestUpgradeHoldsANodeWhoseImageBootsNextAfterAFailedActivation(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "w-metal", "0.2.0")
	l.activateLost = "w2"
	err := l.upgrade(img, "--nodes", "w2")
	if err == nil || !strings.HasPrefix(err.Error(), "w2: ") || !strings.Contains(err.Error(), "sync the ESP: input/output error; "+shortOf(t, img)+" boots next all the same, so it stays cordoned: chalkctl node reboot w2 boots it and releases the node") {
		t.Fatalf("upgrade = %v", err)
	}
	if got, want := l.takeEvents(), []string{"w2 upgrade", "w2 drain", "w2 activate"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	if n := l.nodes["w2"]; !l.cordoned["w2"] || !n.active {
		t.Errorf("w2 cordoned: %v; active: %v", l.cordoned["w2"], n.active)
	}
	if err := l.run("node", "reboot", "w2"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got, want := l.takeEvents(), []string{"w2 drain", "w2 reboot", "w2 uncordon"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	if n := l.nodes["w2"]; l.cordoned["w2"] || n.image != "0.2.0" {
		t.Errorf("w2 cordoned: %v; runs %s", l.cordoned["w2"], n.image)
	}
}

// TestUpgradeReportsAStagedImage says so when a node answers that the image is staged already,
// which its status did not say, and goes on as after an install.
func TestUpgradeReportsAStagedImage(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "w-metal", "0.2.0")
	l.nodes["w1"].alreadyStaged = true
	if err := l.upgrade(img, "--nodes", "w1", "--until", "activate"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if want := "w1: " + shortOf(t, img) + " is staged already, as .staged-chalkos_2.efi; its config is replaced\n"; !strings.Contains(l.ta.stdout.String(), want) {
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
	if err := l.upgrade(testImageOf(t, "cp-metal", "metal", "0.2.0", 4096, nil), "--until", "activate"); err != nil {
		t.Fatal(err)
	}
	// 300 data blocks; the superblock's block, three of digests and their root.
	if n := l.nodes["cp1"]; n.staged != "0.2.0" || n.stagedStoreSize != 300*4096 || n.stagedVeritySize != 5*4096 {
		t.Errorf("cp1 staged %s with %d bytes of store and %d of hash tree", n.staged, n.stagedStoreSize, n.stagedVeritySize)
	}
}

// TestUpgradeWithAnOperatorClientFile upgrades without delivering config when the credentials
// cannot deliver it.
func TestUpgradeWithAnOperatorClientFile(t *testing.T) {
	l := newUpgradeLab(t, 3)
	config := l.ta.writeConfig(t, "ops", pki.RoleOperator, time.Hour, time.Now())
	l.ta.withoutSecrets(t)
	l.nodes["n1"].config = "{}"
	if err := l.upgrade(testUpgradeImage(t, "plain-metal", "0.2.0"), "--config", config, "--until", "activate"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if n := l.nodes["n1"]; n.staged != "0.2.0" || n.stagedConfig != "" {
		t.Errorf("n1 stages %q with the config %q, want 0.2.0 without config", n.staged, n.stagedConfig)
	}
	out := l.ta.stdout.String()
	if line := "config not delivered: the operator role cannot deliver config; each node gives the image its pending config, else carries its own over\n"; strings.Count(out, line) != 1 || strings.Contains(out, "n1: config") {
		t.Errorf("output:\n%s\nwant %q once and no node's config line", out, line)
	}
	err := l.upgrade(testUpgradeImage(t, "plain-metal", "0.2.0"), "--config", config, "--diff")
	if err == nil || !strings.Contains(err.Error(), "upgrade --diff compares the config an upgrade delivers, which needs the admin role") {
		t.Errorf("upgrade --diff as an operator = %v", err)
	}
}

// TestUpgradeWithAnOperatorClientFileAlone upgrades a partial cluster as an operator: there is no
// inventory, so no config to withhold, and each node says it carries its own over.
func TestUpgradeWithAnOperatorClientFileAlone(t *testing.T) {
	l := newUpgradeLab(t, 1)
	config := l.ta.writeConfig(t, "ops", pki.RoleOperator, time.Hour, time.Now())
	args := []string{"upgrade", "--image", testUpgradeImage(t, "w-metal", "0.2.0"), "--config", config, "--nodes", "w1", "--until", "activate"}
	args = append(args, l.addressArgs()...)
	if err := l.ta.run(context.Background(), args); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	out := l.ta.stdout.String()
	if !strings.Contains(out, "w1: no inventory to compare the config with; the node gives the image its pending config, else carries its own over") || strings.Contains(out, "config not delivered") {
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
	err := l.upgrade(testUpgradeImage(t, "w-metal", "0.2.0"), "--config", config, "--nodes", "w1")
	if err == nil || !strings.Contains(err.Error(), "upgrade needs the operator role; the reader role may not upgrade nodes") {
		t.Fatalf("upgrade as a reader = %v", err)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("events %q, want none", got)
	}
}

// TestUpgradeWithholdsConfigANodeDoesNotShow sends no config to nodes that refuse to show theirs,
// and says so once.
func TestUpgradeWithholdsConfigANodeDoesNotShow(t *testing.T) {
	l := newUpgradeLab(t, 1)
	l.configDenied = true
	img := testUpgradeImage(t, "w-metal", "0.2.0")
	err := l.upgrade(img, "--diff")
	if err == nil || !strings.Contains(err.Error(), "w1 refuses to show its config to these credentials; upgrade --diff") {
		t.Errorf("upgrade --diff = %v, want w1's refusal named", err)
	}
	if err := l.upgrade(img, "--nodes", "w1,w2", "--until", "activate", "--max-unavailable", "2"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	for _, name := range []string{"w1", "w2"} {
		if n := l.nodes[name]; n.staged != "0.2.0" || n.stagedConfig != "" {
			t.Errorf("%s stages %q with the config %q, want 0.2.0 without config", name, n.staged, n.stagedConfig)
		}
	}
	out := l.ta.stdout.String()
	if line := "w1 refuses to show its config to these credentials; config not delivered, each node gives the image its pending config, else carries its own over\n"; strings.Count(out, line) != 1 || strings.Contains(out, "role cannot deliver config") {
		t.Errorf("output:\n%s\nwant %q once", out, line)
	}
}

// TestUpgradeWithholdsConfigBeforeDraining withholds config before asking for passwords or draining
// when the first node refuses to show it.
func TestUpgradeWithholdsConfigBeforeDraining(t *testing.T) {
	l := newUpgradeLab(t, 1)
	l.configDenied = true
	img := testUpgradeImage(t, "w-metal", "0.2.0")
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

// TestUpgradeAsksTheFirstNodeItInstallsForItsConfig asks the first node that gets the image, not
// one that runs it, for its config.
func TestUpgradeAsksTheFirstNodeItInstallsForItsConfig(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w-metal", "0.2.0")
	withPasswordVolume(t, l, "w1", "w2")
	prompts := recordPrompts(l)
	l.nodes["w1"].image, l.nodes["w1"].root = "0.2.0", rootOf(t, img)
	l.configErrs = map[string]error{"w2": connect.NewError(connect.CodePermissionDenied, errors.New("GetConfig needs the admin role"))}
	if err := l.upgrade(img, "--nodes", "w1,w2", "--until", "activate"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if asked := l.asked(); asked["w2"] == 0 {
		t.Errorf("asked %v for their config, want w2", asked)
	}
	if len(*prompts) != 0 {
		t.Errorf("asked %q", *prompts)
	}
	if line := "w2 refuses to show its config to these credentials; config not delivered, each node gives the image its pending config, else carries its own over\n"; strings.Count(l.ta.stdout.String(), line) != 1 {
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
	img := testUpgradeImage(t, "w-metal", "0.2.0")
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

// TestUpgradeAsksNoPasswordOfANodeItDoesNotInstall asks no fallback password of a node that is not
// sent the image.
func TestUpgradeAsksNoPasswordOfANodeItDoesNotInstall(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w-metal", "0.2.0")
	withPasswordVolume(t, l, "w1", "w2")
	prompts := recordPrompts(l)
	l.nodes["w1"].image, l.nodes["w1"].root = "0.2.0", rootOf(t, img)
	l.nodes["w2"].staged, l.nodes["w2"].stagedRoot, l.nodes["w2"].stagedConfig = "0.2.0", rootOf(t, img), l.inventoryConfig("w2")
	if err := l.upgrade(img, "--nodes", "w1,w2", "--until", "activate"); err != nil {
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

// TestUpgradeAsksForPasswordsBeforeDraining asks each node's fallback password in turn before any
// node is drained.
func TestUpgradeAsksForPasswordsBeforeDraining(t *testing.T) {
	l := newUpgradeLab(t, 1)
	withPasswordVolume(t, l, "w1", "w2")
	l.ta.answers = []string{"one", "one", "two", "two"}
	recordPrompts(l)
	if err := l.upgrade(testUpgradeImage(t, "w-metal", "0.2.0"), "--nodes", "w1,w2", "--max-unavailable", "2"); err != nil {
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

// TestUpgradeChecksTheConfigBeforeDraining refuses an image the inventory's config does not suit
// before any node changes.
func TestUpgradeChecksTheConfigBeforeDraining(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w-metal", "0.2.0")
	for _, f := range []string{filepath.Join("facts", "kubernetes.json"), filepath.Join("schemas", "kubernetes.chalkos.dev_v1alpha1_Node.json")} {
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
	err = l.upgrade(testUpgradeImage(t, "w-metal", "0.2.0"), "--nodes", "w1,w2")
	if err == nil || !strings.Contains(err.Error(), "w2") || !strings.Contains(err.Error(), "kubernetes.io/role") {
		t.Fatalf("upgrade = %v, want w2's config refused", err)
	}
	if events := l.takeEvents(); len(events) != 0 {
		t.Errorf("the nodes were asked %q", events)
	}
}

// TestImageContractReadsLayerFacts reads the Kubernetes layer's facts from the image directory's
// facts/kubernetes.json alone: a directory without it is an image without Kubernetes.
func TestImageContractReadsLayerFacts(t *testing.T) {
	dir := t.TempDir()
	copySchemas(t, filepath.Join(dir, "schemas"))
	writeFile(t, filepath.Join(dir, "image.json"), testImageJSON)
	if c, err := imageContract(dir); err != nil || len(c.target.Translators) != 0 {
		t.Fatalf("without facts/: contract = %+v, %v", c, err)
	}
	writeFile(t, filepath.Join(dir, "facts", "kubernetes.json"), testImageJSON)
	if c, err := imageContract(dir); err != nil || len(c.target.Translators) == 0 {
		t.Fatalf("contract = %+v, %v", c, err)
	}
	writeFile(t, filepath.Join(dir, "facts", "kubernetes.json"), `{"version": ""}`)
	if _, err := imageContract(dir); err == nil || !strings.Contains(err.Error(), "facts/kubernetes.json") {
		t.Fatalf("broken facts: err = %v", err)
	}
}

// TestUpgradeChecksTheImage refuses an image without a name or with an image version in its UKI
// before any node changes.
func TestUpgradeChecksTheImage(t *testing.T) {
	l := newUpgradeLab(t, 3)
	for _, tc := range []struct {
		name string
		img  string
		want string
	}{
		{"an image version", testImageOf(t, "w-metal", "metal", "0.2.0", 512, func(o map[string]string) { o["IMAGE_VERSION"] = "0.2.0" }), "the image's UKI: the UKI's os-release sets IMAGE_VERSION to \"0.2.0\""},
		{"an image without a name", testUpgradeImage(t, "", "0.2.0"), "the image names no image name or platform in its os-release"},
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
	if err := l.upgrade(testUpgradeImage(t, "w-metal", "0.2.0")); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeRetriesAFailedImage installs an image a node fell back from again with
// --retry-failed, once per run: a second fall back stops the run.
func TestUpgradeRetriesAFailedImage(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.unhealthy["0.3.0"] = true
	img := testUpgradeImage(t, "w-metal", "0.3.0")
	if err := l.upgrade(img); err == nil || !strings.Contains(err.Error(), "rolled back to 010101010101") {
		t.Fatalf("upgrade = %v", err)
	}
	l.takeEvents()
	// Still unhealthy: tried once more, then the run stops.
	if err := l.upgrade(img, "--retry-failed"); err == nil || !strings.Contains(err.Error(), "w1: upgrade to "+shortOf(t, img)+" failed: rolled back to 010101010101") {
		t.Fatalf("a retry of an image that stays unhealthy = %v", err)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"w1 upgrade", "w1 drain", "w1 activate", "w1 reboot"}) {
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
	if err := l.upgrade(testUpgradeImage(t, "w-metal", "0.2.0"), "--nodes", "w1", "--delete-emptydir-data"); err != nil {
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

// TestUpgradeDrainsOnlyNodesWithTheKubernetesLayer neither drains nor uncordons a node without the
// Kubernetes layer.
func TestUpgradeDrainsOnlyNodesWithTheKubernetesLayer(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.nodes["w1"].layers = []string{"router"}
	if err := l.upgrade(testUpgradeImage(t, "w-metal", "0.2.0"), "--nodes", "w1"); err != nil {
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

// TestUpgradeAsksOnlyControlPlanesWithTheKubernetesLayer drains through an --address control plane
// only when it has the Kubernetes layer.
func TestUpgradeAsksOnlyControlPlanesWithTheKubernetesLayer(t *testing.T) {
	l := newUpgradeLab(t, 1)
	config := l.ta.writeConfig(t, "ops", pki.RoleOperator, time.Hour, time.Now())
	upgrade := func() error {
		args := []string{"upgrade", "--image", testUpgradeImage(t, "w-metal", "0.2.0"), "--config", config, "--nodes", "w1"}
		return l.ta.run(context.Background(), append(args, l.addressArgs("cp1", "w1")...))
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
	err := l.upgrade(testUpgradeImage(t, "w-metal", "0.2.0"))
	if err == nil || !strings.Contains(err.Error(), "pods default/web still run: they did not stop") {
		t.Fatalf("upgrade = %v", err)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"w1 upgrade", "w1 drain"}) {
		t.Errorf("events %q", got)
	}
}

// TestUpgradeContinuesOnceReady releases a node a run stopped at after it booted the image only
// once its Node is Ready.
func TestUpgradeContinuesOnceReady(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "w-metal", "0.2.0")
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

// TestUpgradeComparesRootHashes sends the image to nodes that run or stage another build of its
// name.
func TestUpgradeComparesRootHashes(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "w-metal", "0.2.0")
	l.nodes["w1"].image, l.nodes["w1"].root = "0.2.0", bytes.Repeat([]byte{2}, 32)
	l.nodes["w3"].staged, l.nodes["w3"].stagedRoot = "0.2.0", bytes.Repeat([]byte{9}, 32)
	if err := l.upgrade(img); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	events := l.takeEvents()
	for _, w := range []string{"w1", "w3"} {
		if !slices.Contains(events, w+" upgrade") {
			t.Errorf("events %q, want the image sent to %s", events, w)
		}
		if n := l.nodes[w]; !bytes.Equal(n.root, rootOf(t, img)) {
			t.Errorf("%s runs the root hash %x", w, n.root)
		}
	}
}

// TestUpgradeSkipsOtherPlatforms installs an image on the nodes running its name on its platform
// and names those running its name on another platform; a node named on another platform stops
// the run.
func TestUpgradeSkipsOtherPlatforms(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.nodes["w3"].platform = "kvm"
	img := testUpgradeImage(t, "w-metal", "0.2.0")
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
	l.useInventory(t, map[string]string{"w-metal": testUpgradeImage(t, "w-metal", "0.2.0")})
	want := "w2 runs an image built for kvm, but its image w-metal is built for metal; changing a node's platform is a reinstall"
	if err := l.run("upgrade", "--nodes", "w1,w2"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("upgrade of the inventory's images = %v, want %q", err, want)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("asked %q", got)
	}
}

// TestUpgradeWithAClientFileAlone upgrades the --address nodes running the image's name
// without an inventory.
func TestUpgradeWithAClientFileAlone(t *testing.T) {
	l := newUpgradeLab(t, 3)
	config := l.ta.writeConfig(t, "ops", pki.RoleAdmin, time.Hour, time.Now())
	addressed := func(args ...string) []string { return append(args, l.addressArgs()...) }
	if err := l.ta.run(context.Background(), addressed("upgrade", "--config", config)); err == nil || !strings.Contains(err.Error(), "pass --image") {
		t.Errorf("upgrade with a client file alone, without --image = %v", err)
	}
	if err := l.ta.run(context.Background(), []string{"upgrade", "--image", testUpgradeImage(t, "w-metal", "0.2.0"), "--config", config}); err == nil || !strings.Contains(err.Error(), "--address NODE=ADDR") {
		t.Errorf("upgrade without an inventory or addresses = %v", err)
	}
	// A worker is drained through a control plane, which only an address names.
	err := l.ta.run(context.Background(), []string{"upgrade", "--image", testUpgradeImage(t, "w-metal", "0.2.0"), "--config", config, "--address", "w1=" + l.addrs["w1"]})
	if err == nil || !strings.Contains(err.Error(), "give a control plane's address with --address") {
		t.Errorf("upgrade of a worker without a control plane's address = %v", err)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("asked %q", got)
	}
	if err := l.ta.run(context.Background(), addressed("upgrade", "--image", testUpgradeImage(t, "w-metal", "0.2.0"), "--config", config)); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if v := l.images(); v["w1"] != "0.2.0" || v["w2"] != "0.2.0" || v["w3"] != "0.2.0" || v["cp1"] != "0.1.0" || v["n1"] != "0.1.0" {
		t.Errorf("the nodes run %v", v)
	}
	if out := l.ta.stdout.String(); !strings.Contains(out, "w1: no inventory to compare the config with; the node gives the image its pending config, else carries its own over") {
		t.Errorf("output lacks the line without an inventory:\n%s", out)
	}
}

// TestUpgradeNamedNodeOfAnotherImage gives a named node an image of another name: an upgrade
// may change the image a node runs, and the node checks the config it gets against the image.
func TestUpgradeNamedNodeOfAnotherImage(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.upgrade(testUpgradeImage(t, "w-metal", "0.2.0"), "--nodes", "cp1"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if v := l.images(); v["cp1"] != "0.2.0" || v["cp2"] != "0.1.0" || v["w1"] != "0.1.0" {
		t.Errorf("the nodes run %v", v)
	}
	if want := "cp1 runs the image cp-metal; it gets w-metal"; !strings.Contains(l.ta.stdout.String(), want) {
		t.Errorf("stdout = %q, want %q", l.ta.stdout, want)
	}
}

// TestUpgradeWithAnImageGivesItToTheNodesOfItsName installs the image on the nodes running an
// image of its name, and leaves a node of the inventory that runs another one; an image no node
// runs is refused before any node is sent anything.
func TestUpgradeWithAnImageGivesItToTheNodesOfItsName(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.nodes["w2"].imageName = "plain-metal"
	if err := l.upgrade(testUpgradeImage(t, "w-metal", "0.2.0")); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if v := l.images(); v["w1"] != "0.2.0" || v["w2"] != "0.1.0" || v["w3"] != "0.2.0" || v["cp1"] != "0.1.0" || v["n1"] != "0.1.0" {
		t.Errorf("the nodes run %v", v)
	}
	l.takeEvents()
	want := "the cluster has no node running the image node-metal"
	if err := l.upgrade(testUpgradeImage(t, "node-metal", "0.3.0")); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("upgrade with an image no node runs = %v, want %q", err, want)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("asked %q", got)
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
	if err := l.upgrade(testUpgradeImage(t, "w-metal", "0.2.0"), "--nodes", "w1", "--until", "activate"); err != nil {
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
	img := testUpgradeImage(t, "w-metal", "0.2.0")
	l.nodes["w1"].staged, l.nodes["w1"].stagedRoot, l.nodes["w1"].stagedConfig = "0.2.0", rootOf(t, img), "{}"
	if err := l.upgrade(img, "--nodes", "w1", "--until", "activate"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got := l.takeEvents(); !slices.Equal(got, []string{"w1 upgrade", "w1 activate"}) {
		t.Errorf("events %q, want the image sent again", got)
	}
	if got := l.nodes["w1"].stagedConfig; got != l.inventoryConfig("w1") {
		t.Errorf("w1 keeps the config %q, want the inventory's", got)
	}
	if err := l.upgrade(img, "--nodes", "w1", "--until", "activate"); err != nil {
		t.Fatal(err)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("events %q, want the staged image left alone", got)
	}
}

// ApplyConfig keeps the config for the running image, or with staged for the staged one, which
// a node without one holds pending. Config for the running image goes to the staged image too, or
// replaces the pending config; a dry run of it plans both.
func (n *upgradeFake) ApplyConfig(ctx context.Context, req *connect.Request[nodev1.ApplyConfigRequest]) (*connect.Response[nodev1.ApplyConfigResponse], error) {
	if err := needsAdmin(ctx, "ApplyConfig"); err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	switch {
	case req.Msg.DiscardPending:
		var p *nodev1.PendingConfig
		if n.pending != "" {
			p = &nodev1.PendingConfig{Version: versionOf(n.pending), Since: timestamppb.New(pendingSince)}
		}
		n.pending = ""
		n.lab.record("%s discard pending", n.name)
		return connect.NewResponse(&nodev1.ApplyConfigResponse{Pending: p}), nil
	case req.Msg.Staged && n.staged == "" && req.Msg.FallbackSecret != "":
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a pending config is stored without the fallback secret"))
	case req.Msg.Staged && n.staged == "" && req.Msg.DryRun:
		return connect.NewResponse(&nodev1.ApplyConfigResponse{Pending: &nodev1.PendingConfig{Version: versionOf(req.Msg.Config)}}), nil
	case req.Msg.Staged && n.staged == "":
		n.pending = req.Msg.Config
		n.lab.record("%s config apply pending", n.name)
		return connect.NewResponse(&nodev1.ApplyConfigResponse{Pending: &nodev1.PendingConfig{Version: versionOf(n.pending), Since: timestamppb.New(pendingSince)}}), nil
	case req.Msg.Staged && req.Msg.DryRun:
		// The staged image checks the config and plans its version; nothing is kept.
		plan := &nodev1.Plan{Version: versionOf(req.Msg.Config), Replaces: versionOf(n.stagedConfig)}
		return connect.NewResponse(&nodev1.ApplyConfigResponse{Plan: plan, Changes: []*nodev1.StorageChange{{Volume: "extra", Reason: "new"}}}), nil
	case req.Msg.DryRun:
		resp := &nodev1.ApplyConfigResponse{Plan: &nodev1.Plan{Version: versionOf(req.Msg.Config), Replaces: versionOf(n.config)}}
		switch {
		case n.staged != "":
			resp.Staged = &nodev1.StagedSlot{Version: versionOf(req.Msg.Config), Replaces: versionOf(n.stagedConfig)}
		case n.pending != "":
			resp.Pending = &nodev1.PendingConfig{Version: versionOf(req.Msg.Config)}
		}
		return connect.NewResponse(resp), nil
	case !req.Msg.Staged:
		if err := n.lab.refusedConfig(n.name); err != nil {
			return nil, err
		}
		if req.Msg.Try != nil {
			n.trial = &nodev1.ConfigTrial{Version: versionOf(req.Msg.Config), PreviousVersion: versionOf(n.config), Deadline: timestamppb.New(time.Now().Add(req.Msg.Try.AsDuration()))}
			n.trialPrevious = n.config
		}
		n.config = req.Msg.Config
		resp := &nodev1.ApplyConfigResponse{}
		switch {
		case n.staged != "":
			resp.Staged = &nodev1.StagedSlot{Version: versionOf(req.Msg.Config), Replaces: versionOf(n.stagedConfig)}
			n.stagedConfig = req.Msg.Config
		case n.pending != "":
			n.pending = req.Msg.Config
			resp.Pending = &nodev1.PendingConfig{Version: versionOf(n.pending), Since: timestamppb.New(pendingSince)}
		}
		n.lab.record("%s config apply staged=false", n.name)
		n.lab.mu.Lock()
		if k := n.lab.dropAfterDelivery[n.name]; k > 0 {
			if n.lab.unreachable == nil {
				n.lab.unreachable = map[string]int{}
			}
			n.lab.unreachable[n.name] = k
		}
		if n.lab.hangAfterDelivery == n.name {
			n.lab.hanging = n.name
		}
		n.lab.mu.Unlock()
		return connect.NewResponse(resp), nil
	default:
		n.stagedConfig = req.Msg.Config
	}
	n.lab.record("%s config apply staged=%v", n.name, req.Msg.Staged)
	return connect.NewResponse(&nodev1.ApplyConfigResponse{}), nil
}

// Contract is the contract of the image the node runs: the fixture schemas and, on the lab's
// Kubernetes images, image.json.
func (n *upgradeFake) Contract(context.Context, *connect.Request[nodev1.ContractRequest]) (*connect.Response[nodev1.ContractResponse], error) {
	resp := &nodev1.ContractResponse{Platform: n.platform, Schemas: n.lab.schemas}
	if runsKubernetes(n.imageName) {
		facts, err := kubernetes.FactsPayload([]byte(testImageJSON))
		if err != nil {
			return nil, err
		}
		resp.LayerFacts = append(resp.LayerFacts, facts)
	}
	return connect.NewResponse(resp), nil
}

// applyConfig runs chalkctl config apply on the lab's node with the args given.
func (l *upgradeLab) applyConfig(name string, args ...string) error {
	l.ta.stdout.Reset()
	l.ta.stderr.Reset()
	return l.ta.run(context.Background(), append([]string{"config", "apply", name, "--inventory", l.inventory, "--address", l.addrs[name]}, args...))
}

// TestApplyConfigStaged delivers the inventory's config for the staged image, checked against the
// image the inventory names.
func TestApplyConfigStaged(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w-metal", "0.2.0")
	l.useInventory(t, map[string]string{"w-metal": img})
	n := l.nodes["w1"]
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
	n.stagedConfig = "{}"
	if err := l.applyConfig("w1", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if line := "w1: staged image's config " + versionOf("{}") + " → " + want + "\n"; !strings.HasSuffix(l.ta.stdout.String(), line) || n.stagedConfig != "{}" {
		t.Errorf("stdout = %q, want it to end with %q", l.ta.stdout, line)
	}
	if err := l.applyConfig("w1"); err != nil {
		t.Fatal(err)
	}
	if out := l.ta.stdout.String(); !strings.HasSuffix(out, "w1 runs config "+want+"\nw1 boots config "+want+" with its staged image too\n") || n.stagedConfig != l.inventoryConfig("w1") || l.ta.stderr.Len() != 0 {
		t.Errorf("stdout = %q, stderr %q", out, l.ta.stderr)
	}
	n.stagedConfig = "{}"
	l.ta.stdout.Reset()
	if err := l.ta.run(context.Background(), []string{"status", "w1", "--inventory", l.inventory, "--address", l.addrs["w1"]}); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"config " + want + " (the inventory's)", "staged config " + versionOf("{}") + " (not the inventory's; chalkctl config apply --staged w1 delivers it)"} {
		if !strings.Contains(l.ta.stdout.String(), line) {
			t.Errorf("status lacks %q:\n%s", line, l.ta.stdout)
		}
	}
}

// config apply --staged --dry-run prints the staged slot's diff and plan and changes nothing.
func TestApplyConfigStagedDryRun(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w-metal", "0.2.0")
	l.useInventory(t, map[string]string{"w-metal": img})
	n := l.nodes["w1"]
	n.staged, n.stagedRoot, n.stagedConfig = "0.2.0", rootOf(t, img), "{}"
	if err := l.applyConfig("w1", "--staged", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if n.stagedConfig != "{}" {
		t.Errorf("w1 keeps %q for the staged image, want the config it had", n.stagedConfig)
	}
	want := "w1: staged image's config " + versionOf("{}") + " → " + versionOf(l.inventoryConfig("w1")) + "\n" +
		"  storage:  volume extra: new\n"
	if out := l.ta.stdout.String(); !strings.HasSuffix(out, want) || strings.Contains(out, "outputs:") {
		t.Errorf("stdout = %q, want it to end with %q", out, want)
	}
}

// config apply --staged checks the rules that compare the config with the node's against the
// running image's config, as the node does, while its diff compares with the staged image's.
func TestApplyConfigStagedChecksRulesAgainstTheRunningConfig(t *testing.T) {
	l := newUpgradeLab(t, 1)
	img := testUpgradeImage(t, "w-metal", "0.2.0")
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
	l.useInventory(t, map[string]string{"w-metal": img})
	n := l.nodes["w1"]
	want := l.inventoryConfig("w1")
	n.staged, n.stagedRoot, n.stagedConfig = "0.2.0", rootOf(t, img), want
	n.config = strings.Replace(want, `"type":"worker"`, `"type":"controlplane"`, 1)
	err = l.applyConfig("w1", "--staged")
	if err == nil || !strings.Contains(err.Error(), `Node/default: spec.type: the type is fixed`) {
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
	switch {
	case req.Msg.Staged && n.staged != "":
		text = n.stagedConfig
	case req.Msg.Staged && n.pending != "":
		text = n.pending
	case req.Msg.Staged:
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no image is staged, and the node holds no pending config"))
	}
	docs, err := config.Parse(strings.NewReader(text))
	if err != nil {
		return nil, err
	}
	e, err := config.Encode(config.Redact(docs).Documents())
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
	l.useInventory(t, map[string]string{"w-metal": testUpgradeImage(t, "w-metal", "0.2.0")})
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
	img := testUpgradeImage(t, "w-metal", "0.2.0")
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
	if err := l.upgrade(img, "--nodes", "w1", "--until", "activate"); err != nil {
		t.Fatal(err)
	}
	if out := l.ta.stdout.String(); !strings.Contains(out, "w1: config unchanged\nw1: installed ") || strings.Contains(out, "when it first boots") {
		t.Errorf("output:\n%s", out)
	}
}

// TestUpgradeRollsBack boots the nodes' previous image through the upgrade's steps, and a rerun
// changes nothing.
func TestUpgradeRollsBack(t *testing.T) {
	l := newUpgradeLab(t, 3)
	img := testUpgradeImage(t, "w-metal", "0.2.0")
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

// TestUpgradeRollbackContinues resumes a stopped rollback to the same image on the remaining nodes.
func TestUpgradeRollbackContinues(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.upgrade(testUpgradeImage(t, "w-metal", "0.2.0"), "--nodes", "w1,w2"); err != nil {
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

// TestUpgradeRollbackSkipsNodesOnTheTarget leaves nodes alone that run the image a rollback goes
// to.
func TestUpgradeRollbackSkipsNodesOnTheTarget(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.upgrade(testUpgradeImage(t, "w-metal", "0.2.0"), "--nodes", "w1"); err != nil {
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
	for _, line := range []string{"w1: 010101010101 boots next as chalkos_3.efi, without boot counting", "w1: runs 010101010101, found healthy; its boots are not counted\n"} {
		if !strings.Contains(l.ta.stdout.String(), line) {
			t.Errorf("output lacks %q:\n%s", line, l.ta.stdout)
		}
	}
}

// TestUpgradeRollbackRefuses refuses a rollback without one previous image for all nodes, and its
// conflicting flags.
func TestUpgradeRollbackRefuses(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.run("upgrade", "--rollback"); err == nil || !strings.Contains(err.Error(), "no node keeps a previous image to roll back to") {
		t.Errorf("without previous images: %v", err)
	}
	if err := l.upgrade(testUpgradeImage(t, "w-metal", "0.2.0"), "--nodes", "w1,w2"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if err := l.upgrade(testUpgradeImage(t, "w-metal", "0.3.0"), "--nodes", "w1"); err != nil {
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
	img := testUpgradeImage(t, "w-metal", "0.4.0")
	for _, args := range [][]string{{"--image", img}, {"--diff"}, {"--retry-failed"}} {
		if err := l.run(append([]string{"upgrade", "--rollback"}, args...)...); err == nil || !strings.Contains(err.Error(), "--rollback") {
			t.Errorf("--rollback %s = %v, want a usage error", args[0], err)
		}
	}
}

// TestUpgradeRollbackWithoutAReboot stages the previous image with --until activate without
// draining or rebooting.
func TestUpgradeRollbackWithoutAReboot(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.upgrade(testUpgradeImage(t, "w-metal", "0.2.0"), "--nodes", "w1"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	l.takeEvents()
	l.ta.stdout.Reset()
	if err := l.run("upgrade", "--rollback", "--nodes", "w1", "--until", "activate"); err != nil {
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
	if err := l.upgrade(testUpgradeImage(t, "w-metal", "0.2.0"), "--nodes", "w1"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	l.takeEvents()
	l.ta.stdout.Reset()
	if err := l.ta.run(context.Background(), append([]string{"upgrade", "--rollback"}, l.addressArgs("cp1", "w1")...)); err != nil {
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
	img := testUpgradeImage(t, "w-metal", "0.2.0")
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

// TestUpgradeRollbackOfAControlPlane rolls back a control plane as an upgrade takes it down:
// drained after the quorum check and uncordoned once back.
func TestUpgradeRollbackOfAControlPlane(t *testing.T) {
	l := newUpgradeLab(t, 3)
	if err := l.upgrade(testUpgradeImage(t, "cp-metal", "0.2.0"), "--nodes", "cp1"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	l.takeEvents()
	if err := l.run("upgrade", "--rollback", "--nodes", "cp1"); err != nil {
		t.Fatalf("%v\n%s", err, l.ta.stdout)
	}
	if got, want := l.takeEvents(), []string{"cp1 members", "cp1 drain", "cp1 rollback", "cp1 reboot", "cp1 uncordon"}; !slices.Equal(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	if v := l.images()["cp1"]; v != "0.1.0" {
		t.Errorf("cp1 runs %s, want 0.1.0", v)
	}
}

// TestUpgradeRefusesANodeOnTrial stops before anything changes when a selected node runs a config
// on trial, for an upgrade and a rollback alike.
func TestUpgradeRefusesANodeOnTrial(t *testing.T) {
	l := newUpgradeLab(t, 3)
	l.nodes["w2"].trial = &nodev1.ConfigTrial{Version: "v2", PreviousVersion: "v1", Deadline: timestamppb.New(time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC))}
	want := "w2 runs config v2 on trial until 2026-10-29T12:00:00Z; confirm it with chalkctl config confirm w2, or roll it back with chalkctl config rollback w2, first"
	if err := l.upgrade(testUpgradeImage(t, "w-metal", "0.2.0")); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("upgrade = %v, want %q", err, want)
	}
	if err := l.run("upgrade", "--rollback", "--nodes", "w2"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("upgrade --rollback = %v, want %q", err, want)
	}
	if got := l.takeEvents(); len(got) != 0 {
		t.Errorf("the refused runs asked %q", got)
	}
}

// eventOrder returns where each event is in events; a missing one fails the test.
func eventOrder(t *testing.T, events []string) func(string) int {
	return func(e string) int {
		t.Helper()
		i := slices.Index(events, e)
		if i < 0 {
			t.Fatalf("no %q in %q", e, events)
		}
		return i
	}
}

// inOrder reports whether the node was upgraded, drained, activated, rebooted and uncordoned in
// turn.
func inOrder(at func(string) int, node string) bool {
	prev := -1
	for _, step := range []string{"upgrade", "drain", "activate", "reboot", "uncordon"} {
		i := at(node + " " + step)
		if i < prev {
			return false
		}
		prev = i
	}
	return true
}

// runsKubernetes says whether a test image of the name carries the Kubernetes layer: the upgrade
// lab's images cp-*, w-* and k8s-* do.
func runsKubernetes(imageName string) bool {
	kind, _, _ := strings.Cut(imageName, "-")
	return kind == "cp" || kind == "w" || kind == "k8s"
}
