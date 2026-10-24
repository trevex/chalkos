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
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
)

// upgradeRun upgrades nodes to the image given, or to the images the inventory names for them.
type upgradeRun struct {
	a       *app
	cluster *cluster
	// reach reaches the nodes with the run's credentials, at the addresses --address gives.
	reach nodeSet
	// nodes are the nodes to upgrade, as they were when the run started, and groups the same
	// nodes by the image they get.
	nodes  map[string]*upgradeNode
	groups []*upgradeGroup
	// controlPlanes are the cluster's control-plane nodes, which drain and uncordon nodes.
	controlPlanes           []string
	allowDowntime, noReboot bool
	// passwordFile holds the password of nodes whose fallback is a password, which the config's new
	// encrypted volumes are enrolled with.
	passwordFile string
	// retryFailed installs the image again on nodes that fell back from it; each node is upgraded
	// once per run, so a second fall back stops the run.
	retryFailed, deleteEmptyDir             bool
	maxUnavailable                          int
	timeout, drainTimeout, poll, quorumWait time.Duration
	out                                     sync.Mutex
	// role is the role of the credentials. Below admin, a node accepts no config with an upgrade,
	// so with an inventory withheld is set and the run sends none: each node carries its own over.
	// A partial cluster has no config to send, and each node says so. A node that refuses to show
	// its config sets withheld too, should the role chalkctl read be wrong.
	role         pki.Role
	withheld     atomic.Bool
	withholdOnce sync.Once
	// diffing is set for upgrade --diff, which changes nothing.
	diffing bool
}

// upgradeNode is a node to upgrade, by its part in Kubernetes: its kind, and whether it is part
// of a cluster, so its Node exists; the image it gets; and the config it gets for the image, the
// inventory's, checked against the image; nil on a partial cluster, where the node carries its own
// over.
type upgradeNode struct {
	name, kind string
	inCluster  bool
	image      *diskImage
	delivery   *delivery
	// secret is the fallback secret the delivery's new encrypted volumes are enrolled with, asked
	// for before any node is drained; empty when it adds none.
	secret string
}

// upgradeGroup is the nodes that get one image: the image given, or the one an image directory of
// the inventory holds.
type upgradeGroup struct {
	// key tells groups apart: the image directory, the reference by digest of an image in a
	// registry, or empty for the image given.
	key, dir          string
	variant, platform string
	nodes             []string
	image             *diskImage
}

type upgradeFlags struct {
	cluster                              clusterFlags
	secrets                              secretFlags
	config, image, nodes                 string
	passwordFile                         string
	addresses                            addressList
	maxUnavailable                       positiveInt
	allowDowntime, noReboot, retryFailed bool
	diff                                 bool
	deleteEmptyDir                       bool
	timeout                              time.Duration
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
	f := upgradeFlags{addresses: addressList{}, maxUnavailable: 1}
	cmd := a.command(&cobra.Command{
		Use:     "upgrade",
		Short:   "Install new images on the cluster's nodes, one control plane at a time",
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
	fs.Var(&f.maxUnavailable, "max-unavailable", "how many workers and nodes without Kubernetes upgrade at once")
	fs.BoolVar(&f.allowDowntime, "allow-downtime", false, "upgrade one or two control planes, whose etcd loses its quorum and API server is down while one reboots")
	fs.BoolVar(&f.noReboot, "no-reboot", false, "install the images on the nodes without draining or rebooting them; they boot with their next reboot")
	fs.BoolVar(&f.retryFailed, "retry-failed", false, "install an image again, once, on nodes that fell back from it")
	fs.BoolVar(&f.deleteEmptyDir, "delete-emptydir-data", false, "evict pods with emptyDir volumes too, deleting their data")
	fs.DurationVar(&f.timeout, "timeout", 30*time.Minute, "how long to wait for each node to come back healthy and for its drain")
	registerPasswordFile(fs, &f.passwordFile)
	fs.BoolVar(&f.diff, "diff", false, "print what each node's config changes against the config of the image it runs, document by document and field by field, and change nothing; needs the admin role")
	return cmd
}

func (a *app) upgrade(ctx context.Context, f upgradeFlags) error {
	cf, sf, addresses := f.cluster, f.secrets, f.addresses
	c, err := loadCluster(ctx, cf)
	if err != nil {
		return err
	}
	creds, err := a.loadCredentials(ctx, sf, f.config)
	if err != nil {
		return err
	}
	// The nodes refuse an upgrade below the operator role, but only once a worker is drained.
	if role := creds.role(); !role.Allows(pki.RoleOperator) {
		return fmt.Errorf("upgrade needs the %s role; the %s role may not upgrade nodes", pki.RoleOperator, roleLabel(role))
	}
	if err := c.checkAddresses(addresses); err != nil {
		return err
	}
	var img *diskImage
	if f.image != "" {
		if img, err = openUpgradeImage(ctx, f.image, a.stderr); err != nil {
			return err
		}
		defer img.Close()
	} else if c.partial() {
		return errors.New("without --image each node gets the image the inventory names for it, which needs an inventory; pass --image, or --inventory")
	}
	r := &upgradeRun{
		a: a, cluster: c, reach: nodeSet{cluster: c, creds: creds, addresses: addresses},
		allowDowntime: f.allowDowntime, noReboot: f.noReboot, maxUnavailable: int(f.maxUnavailable),
		retryFailed: f.retryFailed, deleteEmptyDir: f.deleteEmptyDir, passwordFile: f.passwordFile,
		timeout: f.timeout, drainTimeout: f.timeout, poll: a.poll(), quorumWait: min(f.timeout, time.Minute),
	}
	var named []string
	if f.nodes != "" {
		named = strings.Split(f.nodes, ",")
	}
	if err := r.find(ctx, named, img); err != nil {
		return err
	}
	for _, g := range r.groups {
		if g.image == nil {
			if g.image, err = openUpgradeImage(ctx, g.dir, a.stderr); err != nil {
				return err
			}
			defer g.image.Close()
			if err := c.sameBuild(g.nodes[0], g.image); err != nil {
				return err
			}
		}
		for _, name := range g.nodes {
			r.nodes[name].image = g.image
		}
	}
	r.role = creds.role()
	switch {
	case !r.role.Allows(pki.RoleAdmin) && f.diff:
		return r.diffNeedsAdmin()
	case !r.role.Allows(pki.RoleAdmin):
		// A partial cluster has no inventory, so there is no config to withhold, and each node
		// says that it carries its own over.
		if !r.cluster.partial() {
			r.withhold("config not delivered: the %s role cannot deliver config; each node carries its own over", r.roleName())
		}
	default:
		if err := r.checkConfig(); err != nil {
			return err
		}
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

// probe reads what a node runs and its status.
func (r *upgradeRun) probe(ctx context.Context, name string) (*nodev1.InfoResponse, *nodev1.StatusResponse, error) {
	var info *nodev1.InfoResponse
	var st *nodev1.StatusResponse
	err := r.call(name, func(conn *client.Conn) error {
		i, err := conn.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
		if err != nil {
			return err
		}
		s, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
		if err != nil {
			return err
		}
		info, st = i.Msg, s.Msg
		return nil
	})
	return info, st, err
}

// find picks the nodes to upgrade and groups them by the image they get. The nodes are those named,
// or else every node of the inventory, or without one every node --address names; with an image and
// no nodes named, those that run the image's variant. Without an image each node gets the image the
// inventory names for it, and the nodes of one image directory form a group. Every node is asked
// what it runs before any is sent anything: one that runs another platform than its image in the
// inventory stops the run. With an image, a node on another platform is skipped and named; a named
// one stops the run. A node may get an image of another variant than it runs, which is printed; the
// node checks the config it gets, or without one its own, against the image before the image
// becomes the next boot.
func (r *upgradeRun) find(ctx context.Context, named []string, img *diskImage) error {
	c := r.cluster
	candidates := named
	if len(candidates) == 0 {
		candidates = r.nodeNames()
	}
	if len(candidates) == 0 && c.partial() {
		return errors.New("without an inventory, give each node's address with --address NODE=ADDR")
	}
	r.nodes = map[string]*upgradeNode{}
	groups := map[string]*upgradeGroup{}
	var skipped []string
	skip := func(name, platform string) error {
		if len(named) > 0 {
			return fmt.Errorf("%s runs on %s; the image is built for %s", name, platform, img.Info.Platform())
		}
		skipped = append(skipped, fmt.Sprintf("%s (%s)", name, platform))
		return nil
	}
	// The control planes a partial cluster has are known by asking them.
	probedCPs := map[string]bool{}
	for _, name := range candidates {
		if err := c.node(name); err != nil {
			return err
		}
		info, st, err := r.probe(ctx, name)
		if err != nil {
			return err
		}
		// Without an image a node gets its image in the inventory, which must be built for the platform
		// it runs.
		if img == nil {
			if err := c.samePlatform(name, info.Platform); err != nil {
				return err
			}
		}
		n := &upgradeNode{name: name}
		if k := st.Kubernetes; k != nil {
			n.kind, n.inCluster = k.Kind, inCluster(k)
		}
		probedCPs[name] = n.kind == kubernetes.KindControlPlane
		if img != nil && len(named) == 0 && info.Variant != img.Info.Variant() {
			continue
		}
		if img != nil && info.Platform != img.Info.Platform() {
			if err := skip(name, info.Platform); err != nil {
				return err
			}
			continue
		}
		g, err := r.groupOf(name, img)
		if err != nil {
			return err
		}
		if groups[g.key] == nil {
			groups[g.key] = g
		}
		g = groups[g.key]
		r.nodes[name] = n
		if info.Variant != "" && info.Variant != g.variant {
			r.say("%s runs the variant %s; it gets the image of %s", name, info.Variant, g.variant)
		}
		g.nodes = append(g.nodes, name)
	}
	if len(skipped) > 0 {
		r.say("skipping %s: the image is built for %s", strings.Join(skipped, ", "), img.Info.Platform())
	}
	if len(r.nodes) == 0 {
		if img != nil {
			return fmt.Errorf("the cluster has no node of the variant %s on %s to upgrade", img.Info.Variant(), img.Info.Platform())
		}
		return errors.New("the cluster has no node to upgrade")
	}
	for _, key := range slices.Sorted(maps.Keys(groups)) {
		r.groups = append(r.groups, groups[key])
	}
	// The control planes drain the nodes and keep etcd's quorum, so every node's type is read,
	// also of nodes the run leaves alone. Loading the inventory read every node's files already,
	// so a node whose files are broken stops every command, and this adds only a node whose
	// files hold the same document twice. Without an inventory, the nodes --address names that
	// the run leaves alone are asked.
	for _, name := range r.nodeNames() {
		if _, probed := probedCPs[name]; c.partial() && !probed {
			_, st, err := r.probe(ctx, name)
			if err != nil {
				return err
			}
			probedCPs[name] = st.Kubernetes.GetKind() == kubernetes.KindControlPlane
		}
		kind, err := c.kind(name)
		if err != nil {
			return err
		}
		if probedCPs[name] || kind == kubernetes.KindControlPlane {
			r.controlPlanes = append(r.controlPlanes, name)
		}
	}
	if len(r.controlPlanes) == 0 && !r.noReboot {
		for _, n := range r.nodes {
			if n.inCluster {
				return fmt.Errorf("%s is drained through a control plane, but the cluster has none to ask; pass --inventory, or give a control plane's address with --address", n.name)
			}
		}
	}
	return nil
}

// nodeNames are the cluster's nodes: the inventory's, or without one those --address names.
func (r *upgradeRun) nodeNames() []string {
	if r.cluster.partial() {
		return slices.Sorted(maps.Keys(r.reach.addresses))
	}
	return r.cluster.nodeNames()
}

// groupOf is the group of the image a node gets: the image given, or the one the inventory names
// for it: from its directory, or from a registry, which the node pulls.
func (r *upgradeRun) groupOf(name string, img *diskImage) (*upgradeGroup, error) {
	if img != nil {
		return &upgradeGroup{variant: img.Info.Variant(), platform: img.Info.Platform(), image: img}, nil
	}
	nc, err := r.cluster.config(name)
	if err != nil {
		return nil, err
	}
	md, err := nc.Resolve()
	if err != nil {
		return nil, err
	}
	if nc.ImageOCI != "" {
		img, err := ociImage(nc, false)
		if err != nil {
			return nil, err
		}
		return &upgradeGroup{key: img.source.GetOci().GetReference(), variant: md.Variant, platform: md.Platform, image: img}, nil
	}
	return &upgradeGroup{key: nc.ImageDir, dir: nc.ImageDir, variant: md.Variant, platform: md.Platform}, nil
}

// split sorts the nodes to upgrade into the control planes and the others, each by name.
func (r *upgradeRun) split() (controlPlanes, others []string) {
	for _, name := range slices.Sorted(maps.Keys(r.nodes)) {
		if r.nodes[name].kind == kubernetes.KindControlPlane {
			controlPlanes = append(controlPlanes, name)
		} else {
			others = append(others, name)
		}
	}
	return controlPlanes, others
}

// order is the order run upgrades the nodes in.
func (r *upgradeRun) order() []string {
	controlPlanes, others := r.split()
	return append(controlPlanes, others...)
}

// run upgrades the control planes one at a time, then the other nodes in batches.
func (r *upgradeRun) run(ctx context.Context) error {
	controlPlanes, others := r.split()
	images := map[string]bool{}
	for _, g := range r.groups {
		r.say("upgrading %s on %s to %s: %s", g.variant, g.platform, g.image.name(), strings.Join(g.nodes, ", "))
		images[g.image.name()] = true
	}
	for _, name := range controlPlanes {
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
	if r.noReboot {
		r.say("installed %s on %s; it boots with their next reboot", names, count(len(r.nodes), "node"))
	} else {
		r.say("upgraded %s to %s", count(len(r.nodes), "node"), names)
	}
	return nil
}

// node upgrades one node, continuing where an earlier run stopped: a node that runs the image
// and was found healthy is uncordoned once its Node is Ready, one that booted it is waited for,
// and the image installed already is not sent again. An image is its store's root hash: a node
// runs, stages or fell back from the image when it names that root hash.
func (r *upgradeRun) node(ctx context.Context, n *upgradeNode) error {
	image, target := n.image.name(), n.image.header.RootHash
	_, st, err := r.probe(ctx, n.name)
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
		// An earlier run may have stopped while the node was not Ready; it is released only once
		// it is.
	case runs:
		r.say("%s: booted %s; waiting for it to be found healthy", n.name, image)
	default:
		if failed {
			r.say("%s: fell back from %s before; trying it again", n.name, image)
		}
		if n.kind == kubernetes.KindControlPlane && !r.noReboot {
			if err := r.quorum(ctx, n.name); err != nil {
				return err
			}
		}
		if n.kind == kubernetes.KindWorker && !r.noReboot {
			if err := r.drain(ctx, n); err != nil {
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
		if n.kind == kubernetes.KindControlPlane {
			if err := r.drain(ctx, n); err != nil {
				return err
			}
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
	return r.uncordon(ctx, n)
}

// stagedAlready reports whether the node has the image staged with the config the run gives it,
// so the image is not sent again: a staged image is sent again only for its config.
func (r *upgradeRun) stagedAlready(n *upgradeNode, st *nodev1.StatusResponse) bool {
	d := r.deliveryOf(n)
	return bytes.Equal(st.GetBoot().GetStaged().GetRootHash(), n.image.header.RootHash) && (d == nil || st.GetStagedConfigVersion() == d.Version)
}

// installs reports whether the run sends the node its image, as node decides from its status.
func (r *upgradeRun) installs(n *upgradeNode, st *nodev1.StatusResponse) bool {
	boot, target := st.GetBoot(), n.image.header.RootHash
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

// quorum checks that etcd keeps its quorum while the control plane reboots: its other voters
// must be healthy and enough for a quorum. With one or two voters the others can never be enough,
// and the API server is down while it reboots, which only --allow-downtime accepts. etcd may need
// a moment after the control plane before came back, so the check is tried for a while.
func (r *upgradeRun) quorum(ctx context.Context, name string) error {
	return waitFor(ctx, "", r.quorumWait, r.poll, func() error { return r.quorumOnce(ctx, name) })
}

func (r *upgradeRun) quorumOnce(ctx context.Context, name string) error {
	var members []*nodev1.EtcdMember
	err := r.viaControlPlanes(name, tryNextControlPlane, func(conn *client.Conn) error {
		resp, err := conn.EtcdMembers(ctx, connect.NewRequest(&nodev1.EtcdMembersRequest{}))
		if err == nil {
			members = resp.Msg.Members
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("etcd's members: %w", err)
	}
	voters, healthy, own := 0, 0, false
	for _, m := range members {
		switch {
		case m.Learner:
			return fmt.Errorf("etcd member %s is a learner yet; upgrade once it joined", m.Name)
		case m.Name == name:
			own = true
		case m.Unhealthy == "":
			healthy++
		}
		voters++
	}
	quorum := voters/2 + 1
	switch {
	case !own:
		// A control plane that is no etcd member takes nothing of etcd down.
		return nil
	case voters-1 < quorum && !r.allowDowntime:
		return permanent{fmt.Errorf("etcd has %s, and without %s fewer than the %d its quorum needs: it and the API server are down while %s reboots; pass --allow-downtime to accept that", count(voters, "voter"), name, quorum, name)}
	case voters-1 < quorum:
		return nil
	case healthy < quorum:
		return fmt.Errorf("without %s etcd has %s of %d, fewer than the %d its quorum needs", name, count(healthy, "healthy voter"), voters, quorum)
	}
	return nil
}

// viaControlPlanes calls fn on a control plane: the node itself first when it is one, then the
// others, moving on past those whose error next accepts.
func (r *upgradeRun) viaControlPlanes(prefer string, next func(error) bool, fn func(conn *client.Conn) error) error {
	names := slices.Clone(r.controlPlanes)
	if i := slices.Index(names, prefer); i > 0 {
		names = append([]string{prefer}, slices.Delete(names, i, i+1)...)
	}
	if len(names) == 0 {
		return errors.New("the cluster has no control-plane node to ask; pass --inventory")
	}
	return r.reach.viaControlPlanes(names, next, fn)
}

// drainElsewhere reports whether a drain that failed so may be run on another control plane: not
// after it timed out, when the node's pods may still be stopping and another drain would only
// wait as long again.
func drainElsewhere(err error) bool {
	return connect.CodeOf(err) != connect.CodeDeadlineExceeded && tryNextControlPlane(err)
}

// drain cordons a node of the cluster and evicts its pods.
func (r *upgradeRun) drain(ctx context.Context, n *upgradeNode) error {
	if !n.inCluster {
		return nil
	}
	var resp *nodev1.DrainNodeResponse
	err := r.viaControlPlanes(n.name, drainElsewhere, func(conn *client.Conn) error {
		res, err := conn.DrainNode(ctx, connect.NewRequest(&nodev1.DrainNodeRequest{Node: n.name, TimeoutSeconds: uint32(r.drainTimeout / time.Second), DeleteEmptydirData: r.deleteEmptyDir}))
		if err == nil {
			resp = res.Msg
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("drain %s: %w", n.name, err)
	}
	cordon := "cordoned"
	if !resp.Marked {
		cordon = "was cordoned before and stays so"
	}
	pods := "pods"
	if len(resp.Evicted) == 1 {
		pods = "pod"
	}
	r.say("%s: %s, evicted %d %s, kept %d", n.name, cordon, len(resp.Evicted), pods, len(resp.Kept)+len(resp.Unmanaged))
	if len(resp.Unmanaged) > 0 {
		r.say("%s: keeps pods without a controller: %s; nothing starts them elsewhere, and they are deleted for good if the node stays down longer than their tolerations allow", n.name, strings.Join(resp.Unmanaged, ", "))
	}
	return nil
}

// uncordon makes a node of the cluster schedulable again, when the upgrade cordoned it.
func (r *upgradeRun) uncordon(ctx context.Context, n *upgradeNode) error {
	if !n.inCluster {
		return nil
	}
	var uncordoned bool
	err := r.viaControlPlanes(n.name, tryNextControlPlane, func(conn *client.Conn) error {
		resp, err := conn.UncordonNode(ctx, connect.NewRequest(&nodev1.UncordonNodeRequest{Node: n.name}))
		if err == nil {
			uncordoned = resp.Msg.Uncordoned
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("uncordon %s: %w", n.name, err)
	}
	if uncordoned {
		r.say("%s: uncordoned", n.name)
	}
	return nil
}

// waitHealthy waits until the node runs the image, its boot was found healthy, and a node in
// the cluster is Ready, or until it fell back.
func (r *upgradeRun) waitHealthy(ctx context.Context, n *upgradeNode) error {
	image, target := n.image.name(), n.image.header.RootHash
	if err := waitFor(ctx, fmt.Sprintf("%s to run %s, found healthy", n.name, image), r.timeout, r.poll, func() error {
		_, st, err := r.probe(ctx, n.name)
		switch {
		case err != nil:
			return err
		case bytes.Equal(st.GetBoot().GetFailed().GetRootHash(), target):
			return permanent{r.rolledBack(n, st.GetBoot())}
		case !bytes.Equal(st.GetBoot().GetRunning().GetRootHash(), target):
			return fmt.Errorf("it runs %s", bootImageName(st.GetBoot().GetRunning()))
		case !st.GetBoot().GetBlessed():
			return errors.New("its boot was not found healthy yet")
		case inCluster(st.Kubernetes) && st.Kubernetes.NodeReady != "True":
			return fmt.Errorf("its Node is not Ready: %s", st.Kubernetes.NodeReady)
		}
		return nil
	}); err != nil {
		return err
	}
	r.say("%s: runs %s, found healthy", n.name, image)
	return nil
}

// inCluster reports whether a node is part of a cluster, so its Node exists.
func inCluster(k *nodev1.KubernetesStatus) bool {
	switch k.GetPhase() {
	case nodev1.KubernetesPhase_KUBERNETES_PHASE_BOOTSTRAPPED, nodev1.KubernetesPhase_KUBERNETES_PHASE_JOINED:
		return true
	}
	return false
}

// rolledBack is the error of a node that fell back from the image, with what its boots logged. A
// node of the cluster stays cordoned, for an operator to look at.
func (r *upgradeRun) rolledBack(n *upgradeNode, boot *nodev1.BootStatus) error {
	msg := fmt.Sprintf("%s: upgrade to %s failed: rolled back to %s", n.name, bootImageName(boot.Failed), bootImageName(boot.Running))
	if n.inCluster && !r.noReboot {
		msg += "; it stays cordoned"
	}
	if len(boot.Journal) > 0 {
		msg += "; its boots logged:\n  " + strings.Join(boot.Journal, "\n  ")
	}
	return errors.New(msg)
}
