package chalkctl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/esp"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
)

// upgradeRun upgrades nodes to the image given, or to the images the inventory names for them:
// each node gets its image staged, activated and booted in the steps chalkctl upgrade --until
// stage, chalkctl node activate and chalkctl node reboot run one at a time, as far as --until
// says.
type upgradeRun struct {
	// rebooter reboots each node into its image, the step chalkctl node reboot runs too.
	*rebooter
	upgradeOptions
	upgradePlan
	// role is the credentials' role. Below admin a node accepts no config with an upgrade, so the
	// run withholds config and each node gives the image its pending config, else carries its own
	// over. A node that refuses to show its config makes the run withhold config too, in case the
	// role chalkctl read is wrong.
	role         pki.Role
	withheld     atomic.Bool
	withholdOnce sync.Once
	// diffing is set for upgrade --diff, which changes nothing.
	diffing bool
	// rollback is set for upgrade --rollback, which boots the nodes' previous image.
	rollback bool
}

// upgradeStep is how far a run takes each node, as --until names it.
type upgradeStep string

const (
	// untilStage stops once the node's image is staged: its store written, its config checked by
	// the image's own checker and kept, its UKI on the ESP under a name the boot loader does not
	// boot.
	untilStage upgradeStep = "stage"
	// untilActivate stops once the image boots next.
	untilActivate upgradeStep = "activate"
	// untilReboot reboots the node into the image, held by its layers, and waits until it is
	// healthy.
	untilReboot upgradeStep = "reboot"
)

// upgradeOptions are how a run upgrades, as its flags give it.
type upgradeOptions struct {
	until upgradeStep
	// retryFailed installs the image again on nodes that fell back from it; each node is upgraded
	// once per run, so a second fall back stops the run.
	retryFailed    bool
	maxUnavailable int
	// passwordFile holds the password of nodes whose fallback is a password, which the config's new
	// encrypted volumes are enrolled with.
	passwordFile string
}

type upgradeFlags struct {
	node           nodeFlags
	image, nodes   string
	passwordFile   string
	maxUnavailable int
	until          upgradeStep
	retryFailed    bool
	diff, rollback bool
	layers         map[string]any
	timeout        time.Duration
}

func (a *app) upgradeCommand() *cobra.Command {
	f := upgradeFlags{until: untilReboot, layers: map[string]any{}}
	cmd := a.command(&cobra.Command{
		Use:     "upgrade",
		Short:   "Install new images on the cluster's nodes, or boot their previous ones again",
		Long:    upgradeLong,
		Example: upgradeExample,
		Args:    cli.NoArgs,
	}, func(a *app, ctx context.Context, pos []string) error { return a.upgrade(ctx, f) })
	fs := cmd.Flags()
	f.node.RegisterClient(fs)
	setAddressUsage(fs, "address of a node's chalkd, `NODE=ADDR`, host or host:port; may be repeated (default each node's address in the inventory)")
	fs.StringVar(&f.image, "image", "", "image to install on the nodes that run its variant, or on the nodes --nodes names whatever they run: a raw image with repart-output.json next to it, or an image directory (default the directory of each node's image in the inventory)")
	fs.StringVar(&f.nodes, "nodes", "", "comma-separated nodes to upgrade (default every node of the cluster, or with --image every node that runs the image's variant)")
	fs.IntVar(&f.maxUnavailable, "max-unavailable", 1, "how many nodes upgrade at once after the nodes that act for the others, such as Kubernetes control planes, which upgrade one at a time")
	for _, p := range plugins {
		if u, ok := p.(layer.UpgradeFlags); ok {
			f.layers[p.Name()] = u.UpgradeFlags(fs)
		}
	}
	fs.Var(cli.Enum(&f.until, "step", untilStage, untilActivate, untilReboot), "until", "how far each node gets: stage installs its image and leaves it staged, which chalkctl node activate makes boot next, with the same --image when the run had one; activate makes it boot next, which chalkctl node reboot boots; reboot drains and reboots the node and waits until it is healthy")
	fs.BoolVar(&f.retryFailed, "retry-failed", false, "install an image again, once, on nodes that fell back from it")
	fs.BoolVar(&f.rollback, "rollback", false, "boot the image the nodes ran before their last upgrade again, which their inactive slots still hold, instead of installing an image")
	fs.DurationVar(&f.timeout, "timeout", 30*time.Minute, "how long to wait for each node to come back healthy and for its drain")
	registerPasswordFile(fs, &f.passwordFile)
	fs.BoolVar(&f.diff, "diff", false, "print what each node's config changes against the config of the image it runs, document by document and field by field, and change nothing; needs the admin role")
	return cmd
}

func (a *app) upgrade(ctx context.Context, f upgradeFlags) error {
	if f.maxUnavailable < 1 {
		return cli.Usagef("--max-unavailable must be at least 1")
	}
	if f.timeout <= 0 {
		return cli.Usagef("--timeout must be positive")
	}
	if f.rollback {
		switch {
		case f.image != "":
			return cli.Usagef("--rollback boots the image the nodes ran before; --image goes without it")
		case f.diff:
			return cli.Usagef("--rollback delivers no config; --diff goes without it")
		case f.retryFailed:
			return cli.Usagef("--retry-failed goes without --rollback")
		case f.until == untilStage:
			return cli.Usagef("--rollback stages no image: it makes the image the nodes ran before boot next; --until activate stops there")
		}
	}
	var named []string
	if f.nodes != "" {
		named = strings.Split(f.nodes, ",")
	}
	s, err := a.reach(ctx, f.node, soleNode(named), true)
	if err != nil {
		return err
	}
	c, creds := s.cluster, s.creds
	// The nodes refuse an upgrade below the operator role, but only once a node is drained.
	if role := creds.role(); !role.Allows(pki.RoleOperator) {
		return fmt.Errorf("upgrade needs the %s role; the %s role may not upgrade nodes", pki.RoleOperator, roleLabel(role))
	}
	var img *diskImage
	if f.image != "" {
		if img, err = openImage(ctx, f.image, false, a.stderr); err != nil {
			return err
		}
		defer img.Close()
	} else if c.Partial() && !f.rollback {
		return errors.New("without --image each node gets the image the inventory names for it, which needs an inventory; pass --image, or --inventory")
	}
	r := &upgradeRun{
		rebooter: &rebooter{
			a: a, cluster: c, reach: s,
			rebootOptions: a.rebootOptions(f.layers, f.timeout),
		},
		upgradeOptions: f.options(), role: creds.role(),
		rollback: f.rollback,
	}
	r.fellBack = r.rolledBack
	if f.rollback {
		if err := r.planRollback(ctx, named); err != nil {
			return err
		}
		return r.run(ctx)
	}
	err = r.plan(ctx, named, img)
	defer r.closeImages()
	if err != nil {
		return err
	}
	if err := r.checkImages(ctx); err != nil {
		return err
	}
	if err := r.chooseConfig(f.diff); err != nil {
		return err
	}
	if f.diff {
		return r.diff(ctx)
	}
	if err := r.showsConfig(ctx); err != nil {
		return err
	}
	if err := r.checkChanges(ctx); err != nil {
		return err
	}
	if err := r.fallbacks(ctx); err != nil {
		return err
	}
	return r.run(ctx)
}

// options are the run's options as the flags give them.
func (f upgradeFlags) options() upgradeOptions {
	return upgradeOptions{until: f.until, maxUnavailable: f.maxUnavailable, retryFailed: f.retryFailed, passwordFile: f.passwordFile}
}

// chooseConfig decides whether the run delivers the inventory's config with the images, which
// only an admin may, and checks it against them before any node is drained. upgrade --diff
// needs the admin role.
func (r *upgradeRun) chooseConfig(diff bool) error {
	switch {
	case !r.role.Allows(pki.RoleAdmin) && diff:
		return r.diffNeedsAdmin()
	case !r.role.Allows(pki.RoleAdmin):
		// A partial cluster has no inventory, so there is no config to withhold, and each node
		// says what it gives the image.
		if !r.cluster.Partial() {
			r.withhold("config not delivered: the %s role cannot deliver config; each node gives the image its pending config, else carries its own over", roleLabel(r.role))
		}
		return nil
	}
	return r.checkConfig()
}

// withhold keeps the run's config and fallback secrets from the nodes, which accept them with an
// upgrade only from an admin, and prints the line that says why once.
func (r *upgradeRun) withhold(format string, args ...any) {
	r.withholdOnce.Do(func() {
		r.withheld.Store(true)
		r.say(format, args...)
	})
}

// withholdDenied withholds the run's config after the node refused to show its config to the
// credentials, which read as admin, so it would refuse config with the upgrade too.
func (r *upgradeRun) withholdDenied(name string) {
	r.withhold("%s refuses to show its config to these credentials; config not delivered, each node gives the image its pending config, else carries its own over", name)
}

// showsConfig asks the first node the run sends its image for its config, once and before any
// fallback secret is asked for or any node is drained, when the run delivers config. A node that
// refuses to show it to credentials that read as admin would refuse config with the upgrade too, so
// the run withholds config from the start: no node is then sent a staged image again for its
// config alone, and no password is asked for. configChanges still withholds should a later node
// refuse.
func (r *upgradeRun) showsConfig(ctx context.Context) error {
	if r.withheld.Load() {
		return nil
	}
	for _, name := range r.order() {
		n := r.nodes[name]
		if n.delivery == nil {
			continue
		}
		var asked, denied bool
		if err := r.reach.Call(name, func(conn *client.Conn) error {
			st, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
			if err != nil {
				return err
			}
			if !r.installs(n, st.Msg) {
				return nil
			}
			asked = true
			_, err = slotConfig(ctx, conn, false)
			if connect.CodeOf(err) == connect.CodePermissionDenied {
				denied = true
				return nil
			}
			return err
		}); err != nil {
			return err
		}
		if denied {
			r.withholdDenied(name)
		}
		if asked {
			return nil
		}
	}
	return nil
}

// roleLabel names a role in messages, as "the ... role".
func roleLabel(role pki.Role) string {
	if role == "" {
		return "client file's"
	}
	return string(role)
}

// diffNeedsAdmin is the error of upgrade --diff with credentials below admin, which may neither
// read a node's config nor deliver one.
func (r *upgradeRun) diffNeedsAdmin() error {
	return fmt.Errorf("upgrade --diff compares the config an upgrade delivers, which needs the admin role; the %s role cannot deliver config, so each node gives the image its pending config, else carries its own over", roleLabel(r.role))
}

// deliveryOf is the config the node gets with its image; nil when the run withholds config or has
// no inventory.
func (r *upgradeRun) deliveryOf(n *upgradeNode) *delivery {
	if r.withheld.Load() {
		return nil
	}
	return n.delivery
}

// diff prints, for every node to upgrade, what its config changes against the running image's,
// which the new image's slot replaces.
func (r *upgradeRun) diff(ctx context.Context) error {
	r.diffing = true
	for _, name := range slices.Sorted(maps.Keys(r.nodes)) {
		var denied bool
		if err := r.reach.Call(name, func(conn *client.Conn) error {
			changes, err := r.configChanges(ctx, conn, r.nodes[name])
			if connect.CodeOf(err) == connect.CodePermissionDenied {
				denied = true
				return nil
			}
			if err != nil || changes == nil {
				return err
			}
			r.out.Lock()
			defer r.out.Unlock()
			changes.write(r.a.stdout)
			return nil
		}); err != nil {
			return err
		}
		// The credentials read as admin, so the node, not the role, refused.
		if denied {
			return fmt.Errorf("%s refuses to show its config to these credentials; upgrade --diff has nothing to compare the inventory's config with", name)
		}
	}
	return nil
}

// configChanges prints the line of what the node's config changes against the running image's and
// returns the changes; none on a partial cluster, which has no inventory to compare with, or when
// the run withholds config. A node that refuses to show its config to the credentials' role would
// refuse config with the upgrade too, so the run withholds it from then on; --diff stops instead.
func (r *upgradeRun) configChanges(ctx context.Context, conn *client.Conn, n *upgradeNode) (configDiff, error) {
	if r.withheld.Load() {
		return nil, nil
	}
	if n.delivery == nil {
		r.say("%s: no inventory to compare the config with; the node gives the image its pending config, else carries its own over", n.name)
		return nil, nil
	}
	nodeDocs, err := slotConfig(ctx, conn, false)
	if connect.CodeOf(err) == connect.CodePermissionDenied && !r.diffing {
		r.withholdDenied(n.name)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	changes := diffConfig(nodeDocs, config.Redact(n.delivery.Documents))
	r.say("%s: %s", n.name, changes.summary())
	return changes, nil
}

// checkConfig checks, before any node is drained or sent its image, that each node's config suits
// the image it gets, as an install checks the config it delivers, and keeps it for the node, which
// gets it with the image. Nodes that run the image already are checked too, as one the run would
// only wait for or uncordon. Each node checks the config against the image again before it keeps
// it; on a partial cluster, which has no inventory, that check of the config the node carries over
// is all there is.
func (r *upgradeRun) checkConfig() error {
	if r.cluster.Partial() {
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(r.nodes)) {
		ct := *r.nodes[name].image.contract
		nc, err := r.cluster.config(name)
		if err != nil {
			return err
		}
		d, err := checkNodeConfig(nc, ct)
		if err != nil {
			return fmt.Errorf("the config of %s: %w", name, err)
		}
		r.a.warnUnpinned(name, d)
		r.nodes[name].delivery = &d
	}
	return nil
}

// run upgrades the coordinators one at a time, then the other nodes in batches.
func (r *upgradeRun) run(ctx context.Context) error {
	coordinators, others := r.split()
	images := map[string]bool{}
	for _, g := range r.groups {
		r.say("upgrading %s on %s to %s: %s", g.variant, g.platform, g.image.name(), strings.Join(g.nodes, ", "))
		images[g.image.name()] = true
	}
	if r.rollback {
		r.say("rolling back %s to %s: %s", layer.Count(len(r.nodes), "node"), bootImageName(r.rollbackTo), strings.Join(r.order(), ", "))
	}
	for _, name := range coordinators {
		if err := r.node(ctx, r.nodes[name]); err != nil {
			return err
		}
	}
	for batch := range slices.Chunk(others, r.maxUnavailable) {
		errs := make([]error, len(batch))
		var wg sync.WaitGroup
		for i, name := range batch {
			wg.Go(func() { errs[i] = r.node(ctx, r.nodes[name]) })
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return err
		}
	}
	names := strings.Join(slices.Sorted(maps.Keys(images)), ", ")
	count := layer.Count(len(r.nodes), "node")
	switch {
	case r.rollback && r.until == untilActivate:
		r.say("staged %s on %s; it boots with their next reboot, which chalkctl node reboot runs, and an upgrade to the image they run leaves it staged", bootImageName(r.rollbackTo), count)
	case r.rollback:
		r.say("rolled back %s to %s", count, bootImageName(r.rollbackTo))
	case r.until == untilStage:
		r.say("staged %s on %s; chalkctl node activate makes it boot next", names, count)
	case r.until == untilActivate:
		r.say("activated %s on %s; it boots with their next reboot, which chalkctl node reboot runs", names, count)
	default:
		r.say("upgraded %s to %s", count, names)
	}
	return nil
}

// node upgrades one node, continuing where an earlier run stopped, in the steps the step-by-step
// path runs: the image is staged, activated, and the node rebooted into it, as far as --until
// says. A node that runs the image and was found healthy is released once it serves its clusters
// again, one that booted it is waited for, and the image staged or active already is not sent
// again. Its layers' hooks hold it before it reboots and release it after. An image is its
// store's root hash: a node runs, stages or fell back from the image when it names that root
// hash.
func (r *upgradeRun) node(ctx context.Context, n *upgradeNode) error {
	image, target := n.target()
	_, st, _, err := r.probe(ctx, n.name)
	if err != nil {
		return err
	}
	boot := st.GetBoot()
	runs := bytes.Equal(boot.GetRunning().GetRootHash(), target)
	failed := bytes.Equal(boot.GetFailed().GetRootHash(), target)
	switch {
	case boot.GetError() != "":
		return fmt.Errorf("%s: its boot is unknown: %s", n.name, boot.GetError())
	case failed && !r.retryFailed:
		return r.rolledBack(&n.rebootNode, boot)
	case runs && boot.GetBlessed():
		if r.until != untilReboot {
			r.say("%s: runs %s", n.name, image)
			return nil
		}
		// An earlier run may have stopped while the node did not serve its clusters; it is released
		// only once it does.
	case runs:
		r.say("%s: booted %s; waiting for it to be found healthy", n.name, image)
	default:
		if failed {
			r.say("%s: fell back from %s before; trying it again", n.name, image)
		}
		sent, err := r.stage(ctx, n, st)
		if err != nil {
			return err
		}
		activate := func() error { return r.activate(ctx, n, st, sent) }
		switch r.until {
		case untilStage:
			return nil
		case untilActivate:
			return activate()
		}
		// The image boots next only once the node's layers hold it.
		return r.reboot(ctx, &n.rebootNode, bootOf(image, target, st), true, activate)
	}
	return r.release(ctx, &n.rebootNode, bootTarget{image: image, root: target}, true)
}

// stage stages the image on the node, as chalkctl upgrade --until stage does: it sends the image
// unless the node has it staged or active already with the config the run gives it, and reports
// whether it sent it. A rollback stages nothing: its image is in the node's inactive slot already.
func (r *upgradeRun) stage(ctx context.Context, n *upgradeNode, st *nodev1.StatusResponse) (bool, error) {
	if n.rollbackTo != nil {
		return false, nil
	}
	// An image staged or active is sent again only for its config: the node finds the image in its
	// inactive slot and replaces the config it keeps for it.
	if r.stagedAlready(n, st) {
		image, _ := n.target()
		r.say("%s: %s is installed already", n.name, image)
		return false, nil
	}
	return true, r.install(ctx, n)
}

// activate makes the image boot next on the node, as chalkctl node activate does, unless the
// node's status said it boots next already and the run did not send it again; a rollback's image
// is made boot next by the rollback, which stages it as an upgrade's activation does.
func (r *upgradeRun) activate(ctx context.Context, n *upgradeNode, st *nodev1.StatusResponse, sent bool) error {
	image, target := n.target()
	boot := st.GetBoot()
	if n.rollbackTo == nil && !sent && boot.GetStagedState() == nodev1.SlotState_SLOT_STATE_ACTIVE && bytes.Equal(boot.GetStaged().GetRootHash(), target) {
		r.say("%s: %s boots next already", n.name, image)
		return nil
	}
	if n.rollbackTo == nil {
		var resp *nodev1.ActivateImageResponse
		if err := r.reach.Call(n.name, func(conn *client.Conn) error {
			res, err := conn.ActivateImage(ctx, connect.NewRequest(&nodev1.ActivateImageRequest{RootHash: target}))
			if err == nil {
				resp = res.Msg
			}
			return err
		}); err != nil {
			return err
		}
		r.say("%s", activatedLine(n.name, image, resp))
		return nil
	}
	if r.stagedAlready(n, st) {
		// A rollback stages its image without tries while the boot the node runs was not found
		// healthy.
		n.uncounted = !st.GetBoot().GetBlessed()
		r.say("%s: %s is staged already", n.name, image)
		return nil
	}
	return r.reactivate(ctx, n)
}

// activatedLine says what an activation of the image on the node did, as resp answers.
func activatedLine(name, image string, resp *nodev1.ActivateImageResponse) string {
	switch {
	case resp.AlreadyInstalled:
		return fmt.Sprintf("%s: runs %s already", name, image)
	case resp.AlreadyActive:
		return fmt.Sprintf("%s: %s boots next already, as %s", name, image, resp.Entry)
	}
	return fmt.Sprintf("%s: activated %s, which boots next as %s", name, image, resp.Entry)
}

// stagedAlready reports whether the node has the image in its inactive slot, staged or active,
// with the config the run gives it, so the image is not sent again: an image the slot holds is
// sent again only for its config.
func (r *upgradeRun) stagedAlready(n *upgradeNode, st *nodev1.StatusResponse) bool {
	d := r.deliveryOf(n)
	_, target := n.target()
	return bytes.Equal(st.GetBoot().GetStaged().GetRootHash(), target) && (d == nil || st.GetStagedConfigVersion() == d.Version)
}

// installs reports whether the run sends the node its image, as node decides from its status.
func (r *upgradeRun) installs(n *upgradeNode, st *nodev1.StatusResponse) bool {
	boot := st.GetBoot()
	_, target := n.target()
	switch {
	case boot.GetError() != "", bytes.Equal(boot.GetRunning().GetRootHash(), target):
		return false
	case bytes.Equal(boot.GetFailed().GetRootHash(), target) && !r.retryFailed:
		return false
	}
	return !r.stagedAlready(n, st)
}

// fallbacks asks for the fallback secret of each node whose config adds an encrypted volume and
// that gets its image, one node after the other and before any node is drained: passwords typed on
// the terminal are asked for in turn, not by the nodes of a batch at once, and no drained node
// waits for them.
func (r *upgradeRun) fallbacks(ctx context.Context) error {
	for _, name := range slices.Sorted(maps.Keys(r.nodes)) {
		n := r.nodes[name]
		d := r.deliveryOf(n)
		if d == nil || d.identity.Storage.Fallback == storage.FallbackNone {
			continue
		}
		if err := r.reach.Call(name, func(conn *client.Conn) error {
			st, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
			if err != nil {
				return err
			}
			if !r.installs(n, st.Msg) {
				return nil
			}
			t, err := r.reach.target(name)
			if err != nil {
				return err
			}
			n.secret, err = r.a.fallbackOf(ctx, st.Msg, t, d.identity.Storage, r.passwordFile, "")
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// install sends the node its image with its config, and the fallback secret when the config adds
// an encrypted volume, which the node creates before the image boots. The node stages the image
// and stops there; activate makes it boot next.
func (r *upgradeRun) install(ctx context.Context, n *upgradeNode) error {
	name, img := n.name, n.image
	parts := io.MultiReader()
	if ref := img.source.GetOci().GetReference(); ref != "" {
		r.say("%s: installing %s, which it pulls from %s", name, img.name(), ref)
	} else {
		r.say("%s: installing %s", name, img.name())
		stream, done, err := img.stream()
		if err != nil {
			return err
		}
		defer done()
		parts = stream
	}
	var resp *nodev1.UpgradeResponse
	err := r.reach.Call(name, func(conn *client.Conn) error {
		if _, err := r.configChanges(ctx, conn, n); err != nil {
			return err
		}
		header := &nodev1.UpgradeHeader{Image: img.header, Source: img.source, StageOnly: true}
		if d := r.deliveryOf(n); d != nil {
			header.Config, header.FallbackSecret = d.Text, n.secret
		}
		stream := conn.Upgrade(ctx)
		if err := stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Header{Header: header}}); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if err := sendChunks(parts, func(c *nodev1.ImageChunk) error {
			return stream.Send(&nodev1.UpgradeRequest{Message: &nodev1.UpgradeRequest_Chunk{Chunk: c}})
		}); err != nil {
			return err
		}
		res, err := stream.CloseAndReceive()
		if err != nil {
			return err
		}
		resp = res.Msg
		return nil
	})
	if err != nil {
		return err
	}
	switch {
	case resp.AlreadyInstalled:
		r.say("%s: runs %s already", name, img.name())
	case resp.AlreadyStaged && resp.Active:
		r.say("%s: %s boots next already, as %s; its config is replaced", name, img.name(), resp.Entry)
	case resp.AlreadyStaged:
		r.say("%s: %s is staged already, as %s; its config is replaced", name, img.name(), resp.Entry)
	default:
		r.say("%s: installed %s, staged as %s", name, img.name(), resp.Entry)
	}
	if note := pendingOutcome(name, resp.Pending); note != "" {
		r.say("%s", note)
	}
	return nil
}

// pendingOutcome says what an upgrade did with p, the pending config the node held as it began, as
// the node answers: the image took it, the config the upgrade delivered replaced it, or the node
// keeps it for a later upgrade, as when the upgrade wrote no config slot or the node failed to
// remove it. The node answers, rather than a later Status, since it may reboot right after. Empty
// without a pending config.
func pendingOutcome(name string, p *nodev1.PendingOutcome) string {
	switch p.GetFate() {
	case nodev1.PendingFate_PENDING_FATE_TAKEN:
		return fmt.Sprintf("%s: the image took the pending config %s, which it checked first", name, p.Version)
	case nodev1.PendingFate_PENDING_FATE_REPLACED:
		return fmt.Sprintf("%s: the inventory's config replaced the pending config %s", name, p.Version)
	case nodev1.PendingFate_PENDING_FATE_KEPT:
		return fmt.Sprintf("%s: keeps the pending config %s for a later upgrade", name, p.Version)
	}
	return ""
}

// reactivate has the node boot the rollback's image next again, its previous image.
func (r *upgradeRun) reactivate(ctx context.Context, n *upgradeNode) error {
	image, root := n.target()
	r.say("%s: rolling back to %s", n.name, image)
	var resp *nodev1.RollbackImageResponse
	if err := r.reach.Call(n.name, func(conn *client.Conn) error {
		res, err := conn.RollbackImage(ctx, connect.NewRequest(&nodev1.RollbackImageRequest{RootHash: root}))
		if err == nil {
			resp = res.Msg
		}
		return err
	}); err != nil {
		return err
	}
	n.uncounted = !resp.AlreadyInstalled && !esp.Counted(resp.Entry)
	switch {
	case resp.AlreadyInstalled:
		r.say("%s: runs %s already", n.name, image)
	case resp.AlreadyStaged && n.uncounted:
		r.say("%s: %s is staged already, as %s, %s", n.name, image, resp.Entry, uncountedRollback)
	case resp.AlreadyStaged:
		r.say("%s: %s is staged already, as %s", n.name, image, resp.Entry)
	case n.uncounted:
		r.say("%s: %s boots next as %s, %s", n.name, image, resp.Entry, uncountedRollback)
	default:
		r.say("%s: %s boots next as %s", n.name, image, resp.Entry)
	}
	return nil
}

// uncountedRollback says why a rollback's entry carries no tries.
const uncountedRollback = "without boot counting: the boot the node runs was not found healthy, so the image before, found healthy once, is its known-good image again and is not fallen back from"

// rolledBack is the error of a node that fell back from the image an upgrade or a rollback booted,
// with what its boots logged. A node that serves a cluster stays held there, for an operator to
// look at.
func (r *upgradeRun) rolledBack(n *rebootNode, boot *nodev1.BootStatus) error {
	msg := fmt.Sprintf("%s: upgrade to %s failed: rolled back to %s", n.name, bootImageName(boot.Failed), bootImageName(boot.Running))
	if r.rollback {
		msg = fmt.Sprintf("%s: rollback to %s failed: it fell back to %s", n.name, bootImageName(boot.Failed), bootImageName(boot.Running))
	}
	return fellBackError(n, msg, boot)
}
