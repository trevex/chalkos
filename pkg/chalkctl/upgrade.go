package chalkctl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
)

// upgradeRun upgrades nodes to the image given, or to the images the inventory names for them.
type upgradeRun struct {
	a       *app
	cluster *cluster
	// reach reaches the nodes with the run's credentials, at the addresses --address gives.
	reach nodeSet
	upgradeOptions
	upgradePlan
	// role is the credentials' role. Below admin a node accepts no config with an upgrade, so the
	// run withholds config and each node carries its own over. A node that refuses to show its
	// config makes the run withhold config too, in case the role chalkctl read is wrong.
	role         pki.Role
	withheld     atomic.Bool
	withholdOnce sync.Once
	// diffing is set for upgrade --diff, which changes nothing.
	diffing bool
	// rollback is set for upgrade --rollback, which boots the nodes' previous image.
	rollback bool
	out      sync.Mutex
}

// upgradeOptions are how a run upgrades, as its flags give it.
type upgradeOptions struct {
	noReboot bool
	// retryFailed installs the image again on nodes that fell back from it; each node is upgraded
	// once per run, so a second fall back stops the run.
	retryFailed    bool
	maxUnavailable int
	// layerFlags are the values of the layers' flags, by layer, as their UpgradeFlags returned them.
	layerFlags map[string]any
	// passwordFile holds the password of nodes whose fallback is a password, which the config's new
	// encrypted volumes are enrolled with.
	passwordFile                            string
	timeout, drainTimeout, poll, quorumWait time.Duration
}

type upgradeFlags struct {
	cluster               clusterFlags
	secrets               secretFlags
	config, image, nodes  string
	passwordFile          string
	addresses             addressList
	maxUnavailable        positiveInt
	noReboot, retryFailed bool
	diff, rollback        bool
	layers                map[string]any
	timeout               time.Duration
}

// positiveInt is the value of an integer flag that is at least 1.
type positiveInt int

func (n *positiveInt) Set(s string) error {
	v, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	if v < 1 {
		return errors.New("must be at least 1")
	}
	*n = positiveInt(v)
	return nil
}

func (n *positiveInt) String() string { return strconv.Itoa(int(*n)) }
func (n *positiveInt) Type() string   { return "int" }

func (a *app) upgradeCommand() *cobra.Command {
	f := upgradeFlags{addresses: addressList{}, maxUnavailable: 1, layers: map[string]any{}}
	cmd := a.command(&cobra.Command{
		Use:     "upgrade",
		Short:   "Install new images on the cluster's nodes, or boot their previous ones again",
		Long:    upgradeLong,
		Example: upgradeExample,
		Args:    cli.NoArgs,
	}, func(a *app, ctx context.Context, pos []string) error { return a.upgrade(ctx, f) })
	fs := cmd.Flags()
	f.cluster.register(fs)
	f.secrets.register(fs)
	registerConfig(fs, &f.config)
	registerAddresses(fs, f.addresses)
	fs.StringVar(&f.image, "image", "", "image to install on the nodes that run its variant, or on the nodes --nodes names whatever they run: a raw image with repart-output.json next to it, or an image directory (default the directory of each node's image in the inventory)")
	fs.StringVar(&f.nodes, "nodes", "", "comma-separated nodes to upgrade (default every node of the cluster, or with --image every node that runs the image's variant)")
	fs.Var(&f.maxUnavailable, "max-unavailable", "how many nodes upgrade at once after the nodes that act for the others, such as Kubernetes control planes, which upgrade one at a time")
	for _, p := range plugins {
		if u, ok := p.(layer.UpgradeFlags); ok {
			f.layers[p.Name()] = u.UpgradeFlags(fs)
		}
	}
	fs.BoolVar(&f.noReboot, "no-reboot", false, "install the images on the nodes without draining or rebooting them; they boot with their next reboot")
	fs.BoolVar(&f.retryFailed, "retry-failed", false, "install an image again, once, on nodes that fell back from it")
	fs.BoolVar(&f.rollback, "rollback", false, "boot the image the nodes ran before their last upgrade again, which their inactive slots still hold, instead of installing an image")
	fs.DurationVar(&f.timeout, "timeout", 30*time.Minute, "how long to wait for each node to come back healthy and for its drain")
	registerPasswordFile(fs, &f.passwordFile)
	fs.BoolVar(&f.diff, "diff", false, "print what each node's config changes against the config of the image it runs, document by document and field by field, and change nothing; needs the admin role")
	return cmd
}

func (a *app) upgrade(ctx context.Context, f upgradeFlags) error {
	if f.rollback {
		switch {
		case f.image != "":
			return cli.Usagef("--rollback boots the image the nodes ran before; --image goes without it")
		case f.diff:
			return cli.Usagef("--rollback delivers no config; --diff goes without it")
		case f.retryFailed:
			return cli.Usagef("--retry-failed goes without --rollback")
		}
	}
	c, err := loadCluster(ctx, f.cluster)
	if err != nil {
		return err
	}
	creds, err := a.loadCredentials(ctx, f.secrets, f.config)
	if err != nil {
		return err
	}
	// The nodes refuse an upgrade below the operator role, but only once a node is drained.
	if role := creds.role(); !role.Allows(pki.RoleOperator) {
		return fmt.Errorf("upgrade needs the %s role; the %s role may not upgrade nodes", pki.RoleOperator, roleLabel(role))
	}
	if err := c.checkAddresses(f.addresses); err != nil {
		return err
	}
	var img *diskImage
	if f.image != "" {
		if img, err = openImage(ctx, f.image, false, a.stderr); err != nil {
			return err
		}
		defer img.Close()
	} else if c.partial() && !f.rollback {
		return errors.New("without --image each node gets the image the inventory names for it, which needs an inventory; pass --image, or --inventory")
	}
	r := &upgradeRun{
		a: a, cluster: c, reach: nodeSet{cluster: c, creds: creds, addresses: f.addresses},
		upgradeOptions: f.options(a.poll()), role: creds.role(),
		rollback: f.rollback,
	}
	var named []string
	if f.nodes != "" {
		named = strings.Split(f.nodes, ",")
	}
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
	if err := r.chooseConfig(f.diff); err != nil {
		return err
	}
	if f.diff {
		return r.diff(ctx)
	}
	if err := r.showsConfig(ctx); err != nil {
		return err
	}
	if err := r.fallbacks(ctx); err != nil {
		return err
	}
	return r.run(ctx)
}

// options are the run's options as the flags give them; poll is the time between two looks at a
// node waited for.
func (f upgradeFlags) options(poll time.Duration) upgradeOptions {
	return upgradeOptions{
		noReboot: f.noReboot, maxUnavailable: int(f.maxUnavailable), retryFailed: f.retryFailed,
		layerFlags: f.layers, passwordFile: f.passwordFile,
		timeout: f.timeout, drainTimeout: f.timeout, poll: poll, quorumWait: min(f.timeout, time.Minute),
	}
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
		// says that it carries its own over.
		if !r.cluster.partial() {
			r.withhold("config not delivered: the %s role cannot deliver config; each node carries its own over", r.roleName())
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
	r.withhold("%s refuses to show its config to these credentials; config not delivered, each node carries its own over", name)
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
		if err := r.call(name, func(conn *client.Conn) error {
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

// roleName names the credentials' role in messages.
func (r *upgradeRun) roleName() string {
	return roleLabel(r.role)
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
	return fmt.Errorf("upgrade --diff compares the config an upgrade delivers, which needs the admin role; the %s role cannot deliver config, so each node carries its own over", r.roleName())
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
		if err := r.call(name, func(conn *client.Conn) error {
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
		r.say("%s: no inventory to compare the config with; the node carries its own over", n.name)
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
	changes := diffConfig(nodeDocs, n.delivery.Documents)
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
	if r.cluster.partial() {
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
		r.nodes[name].delivery = &d
	}
	return nil
}

// say prints a line of the run's progress, from any node's goroutine.
func (r *upgradeRun) say(format string, args ...any) {
	r.out.Lock()
	defer r.out.Unlock()
	fmt.Fprintf(r.a.stdout, format+"\n", args...)
}

// call runs fn against a node; fn's error names the node.
func (r *upgradeRun) call(name string, fn func(conn *client.Conn) error) error {
	return r.reach.call(name, func(conn *client.Conn) error {
		if err := fn(conn); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	})
}

// probe reads what a node runs, its status and its roles in the clusters of its layers.
func (r *upgradeRun) probe(ctx context.Context, name string) (*nodev1.InfoResponse, *nodev1.StatusResponse, upgradeRoles, error) {
	var info *nodev1.InfoResponse
	var st *nodev1.StatusResponse
	var roles upgradeRoles
	err := r.call(name, func(conn *client.Conn) error {
		i, err := conn.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
		if err != nil {
			return err
		}
		s, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
		if err != nil {
			return err
		}
		for _, u := range pluginsOf[layer.Upgrade](i.Msg.Layers) {
			role, err := u.UpgradeRole(ctx, conn)
			if err != nil {
				return err
			}
			roles = append(roles, upgradeRole{u, role})
		}
		info, st = i.Msg, s.Msg
		return nil
	})
	return info, st, roles, err
}

// ready checks, on conn, that the node serves the cluster of each of its layers again.
func (n *upgradeNode) ready(ctx context.Context, conn *client.Conn) error {
	for _, r := range n.roles {
		if err := r.u.Ready(ctx, conn, r.role); err != nil {
			return fmt.Errorf("%s: %w", n.name, err)
		}
	}
	return nil
}

// noCoordinator is the error of the first of the node's layers that needs a coordinator for it,
// a member; nil when none does.
func (n *upgradeNode) noCoordinator(name string) error {
	for _, r := range n.roles {
		if r.role.Member {
			return r.u.NoCoordinator(name)
		}
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
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = r.node(ctx, r.nodes[name])
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return err
		}
	}
	names := strings.Join(slices.Sorted(maps.Keys(images)), ", ")
	switch {
	case r.rollback && r.noReboot:
		r.say("staged %s on %s; it boots with their next reboot", bootImageName(r.rollbackTo), layer.Count(len(r.nodes), "node"))
	case r.rollback:
		r.say("rolled back %s to %s", layer.Count(len(r.nodes), "node"), bootImageName(r.rollbackTo))
	case r.noReboot:
		r.say("installed %s on %s; it boots with their next reboot", names, layer.Count(len(r.nodes), "node"))
	default:
		r.say("upgraded %s to %s", layer.Count(len(r.nodes), "node"), names)
	}
	return nil
}

// node upgrades one node, continuing where an earlier run stopped: a node that runs the image
// and was found healthy is released once it serves its clusters again, one that booted it is
// waited for, and the image installed already is not sent again. Its layers' hooks hold it before
// it reboots and release it after. An image is its store's root hash: a node runs, stages or fell
// back from the image when it names that root hash.
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
		return r.rolledBack(n, boot)
	case runs && boot.GetBlessed():
		if r.noReboot {
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
		if !r.noReboot {
			if err := r.hooks(ctx, n, layer.Upgrade.BeforeInstall); err != nil {
				return err
			}
		}
		// A staged image is sent again only for its config: the node finds the image staged and
		// replaces the config it keeps for it.
		if r.stagedAlready(n, st) {
			r.say("%s: %s is installed already", n.name, image)
		} else if err := r.install(ctx, n); err != nil {
			return err
		}
		if r.noReboot {
			return nil
		}
		if err := r.hooks(ctx, n, layer.Upgrade.BeforeReboot); err != nil {
			return err
		}
		if err := r.call(n.name, func(conn *client.Conn) error {
			_, err := conn.Reboot(ctx, connect.NewRequest(&nodev1.RebootRequest{}))
			return err
		}); err != nil {
			return err
		}
		r.say("%s: rebooting into %s", n.name, image)
	}
	if err := r.waitHealthy(ctx, n); err != nil {
		return err
	}
	return r.hooks(ctx, n, layer.Upgrade.After)
}

// stagedAlready reports whether the node has the image staged with the config the run gives it,
// so the image is not sent again: a staged image is sent again only for its config.
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
		if err := r.call(name, func(conn *client.Conn) error {
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
// an encrypted volume, which the node creates before the image boots.
func (r *upgradeRun) install(ctx context.Context, n *upgradeNode) error {
	if n.rollbackTo != nil {
		return r.reactivate(ctx, n)
	}
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
	err := r.call(name, func(conn *client.Conn) error {
		if _, err := r.configChanges(ctx, conn, n); err != nil {
			return err
		}
		header := &nodev1.UpgradeHeader{Image: img.header, Source: img.source}
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
		if err == nil {
			resp = res.Msg
		}
		return err
	})
	if err != nil {
		return err
	}
	switch {
	case resp.AlreadyInstalled:
		r.say("%s: runs %s already", name, img.name())
	case resp.AlreadyStaged:
		r.say("%s: %s is staged already, as %s", name, img.name(), resp.Entry)
	default:
		r.say("%s: installed %s, which boots next as %s", name, img.name(), resp.Entry)
		// The image's renderers and the list of its outputs are in its own store, which runs once it
		// boots; chalkctl reads neither.
		r.say("%s: %s renders and checks any outputs it declares when it first boots; if one fails, the node falls back to the image it runs", name, img.name())
	}
	return nil
}

// reactivate has the node boot the rollback's image next again, its previous image.
func (r *upgradeRun) reactivate(ctx context.Context, n *upgradeNode) error {
	image, root := n.target()
	r.say("%s: rolling back to %s", n.name, image)
	var resp *nodev1.RollbackImageResponse
	if err := r.call(n.name, func(conn *client.Conn) error {
		res, err := conn.RollbackImage(ctx, connect.NewRequest(&nodev1.RollbackImageRequest{RootHash: root}))
		if err == nil {
			resp = res.Msg
		}
		return err
	}); err != nil {
		return err
	}
	switch {
	case resp.AlreadyInstalled:
		r.say("%s: runs %s already", n.name, image)
	case resp.AlreadyStaged:
		r.say("%s: %s is staged already, as %s", n.name, image, resp.Entry)
	case counted(resp.Entry):
		r.say("%s: %s boots next as %s", n.name, image, resp.Entry)
	default:
		r.say("%s: %s boots next as %s, without boot counting: the boot the node runs was not found healthy, so the image before, found healthy once, is its known-good image again and is not fallen back from", n.name, image, resp.Entry)
	}
	return nil
}

// counted reports whether systemd-boot counts the boots of the entry, whose name then gives its
// tries, as in chalkos_3+3.efi.
func counted(entry string) bool {
	return strings.Contains(strings.TrimSuffix(entry, ".efi"), "+")
}

// waitHealthy waits until the node runs the image, its boot was found healthy, and it serves the
// clusters of its layers again, or until it fell back.
func (r *upgradeRun) waitHealthy(ctx context.Context, n *upgradeNode) error {
	image, target := n.target()
	var fellBack error
	err := layer.WaitFor(ctx, "", r.timeout, r.poll, func() error {
		var st *nodev1.StatusResponse
		var notReady error
		err := r.call(n.name, func(conn *client.Conn) error {
			s, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
			if err != nil {
				return err
			}
			st = s.Msg
			if boot := st.GetBoot(); bytes.Equal(boot.GetRunning().GetRootHash(), target) && boot.GetBlessed() {
				notReady = n.ready(ctx, conn)
			}
			return nil
		})
		switch {
		case err != nil:
			return err
		case bytes.Equal(st.GetBoot().GetFailed().GetRootHash(), target):
			fellBack = r.rolledBack(n, st.GetBoot())
			return layer.Permanent{Err: fellBack}
		case !bytes.Equal(st.GetBoot().GetRunning().GetRootHash(), target):
			return fmt.Errorf("it runs %s", bootImageName(st.GetBoot().GetRunning()))
		case !st.GetBoot().GetBlessed():
			return errors.New("its boot was not found healthy yet")
		}
		return notReady
	})
	switch {
	case fellBack != nil:
		return fellBack
	case err != nil:
		return fmt.Errorf("%s: waiting for %s to be found healthy: %w", n.name, image, err)
	}
	r.say("%s: runs %s, found healthy", n.name, image)
	return nil
}

// rolledBack is the error of a node that fell back from the image, with what its boots logged. A
// node that serves a cluster stays held there, for an operator to look at.
func (r *upgradeRun) rolledBack(n *upgradeNode, boot *nodev1.BootStatus) error {
	msg := fmt.Sprintf("%s: upgrade to %s failed: rolled back to %s", n.name, bootImageName(boot.Failed), bootImageName(boot.Running))
	if r.rollback {
		msg = fmt.Sprintf("%s: rollback to %s failed: it fell back to %s", n.name, bootImageName(boot.Failed), bootImageName(boot.Running))
	}
	if n.roles.member() && !r.noReboot {
		msg += "; it stays cordoned"
	}
	if len(boot.Journal) > 0 {
		msg += "; its boots logged:\n  " + strings.Join(boot.Journal, "\n  ")
	}
	return errors.New(msg)
}

// hooks runs a hook of each of the node's layers, in their order.
func (r *upgradeRun) hooks(ctx context.Context, n *upgradeNode, hook func(layer.Upgrade, context.Context, layer.Upgrading, string, layer.UpgradeRole) error) error {
	for _, role := range n.roles {
		if err := hook(role.u, ctx, r, n.name, role.role); err != nil {
			return err
		}
	}
	return nil
}

var _ layer.Upgrading = (*upgradeRun)(nil)

func (r *upgradeRun) Say(format string, args ...any) { r.say(format, args...) }
func (r *upgradeRun) Flags(layer string) any         { return r.layerFlags[layer] }
func (r *upgradeRun) DrainTimeout() time.Duration    { return r.drainTimeout }
func (r *upgradeRun) QuorumWait() time.Duration      { return r.quorumWait }
func (r *upgradeRun) Poll() time.Duration            { return r.poll }

// ViaCoordinators calls fn on a coordinator: the node itself first when it is one, then the
// others, moving on past those whose error next accepts.
func (r *upgradeRun) ViaCoordinators(prefer string, next func(error) bool, fn func(conn *client.Conn) error) error {
	names := slices.Clone(r.coordinators)
	if i := slices.Index(names, prefer); i > 0 {
		names = append([]string{prefer}, slices.Delete(names, i, i+1)...)
	}
	if len(names) == 0 {
		return layer.ErrNoCoordinator
	}
	return r.reach.viaFirst(names, next, fn)
}
