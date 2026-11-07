package chalkctl

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/anypb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/internal/poll"
)

// rolloutTrial is how long an ordered delivery keeps a coordinator's config on trial without
// --try: long enough for its components to pull their images and start, which Delivered waits for.
const rolloutTrial = 10 * time.Minute

// rolloutQuorumWait is how long an ordered delivery waits for a layer's hold before a coordinator
// gets the config, such as etcd keeping its quorum. Tests lower it.
var rolloutQuorumWait = time.Minute

// applyStep is how far apply-config takes the nodes, as --until names it.
type applyStep string

const (
	// untilDeliver delivers the config to the first node that does not run it, on trial, and leaves
	// the trial open for apply-config --confirm.
	untilDeliver applyStep = "deliver"
	// untilConfirm delivers the config to every node and confirms each trial.
	untilConfirm applyStep = "confirm"
)

// configRollout delivers the inventory's config to several nodes, one after the other, and stops
// at the first that fails. When the config changes the versions a layer's cluster bounds, such as
// the Kubernetes API server's, the run is ordered: the layer checks the versions its nodes run
// first, then the coordinators get the config one at a time, each held by the layer and on trial
// until the layer finds it serves its cluster with the config, and the other nodes after them.
// No node reboots. A node that runs the config already is left alone, so the same command run
// again continues where a run stopped.
type configRollout struct {
	*rebooter
	f applyConfigFlags
	// ordered are the layers whose bounded versions the config changes; none for a run that
	// delivers node by node in the order given.
	ordered []layer.Rollout
	// nodes are the run's nodes by name.
	nodes map[string]*rolloutNode
}

// rolloutNode is a node of a run with what the run read of it before it delivered anything.
type rolloutNode struct {
	rebootNode
	// layers are the layers of the image it runs, as its Info names them.
	layers []string
	// version is the version of the config the run delivers it, and runs the version it runs.
	version, runs string
}

// deliverToNodes delivers the inventory's config to the nodes named, as configRollout does.
func (a *app) deliverToNodes(ctx context.Context, f applyConfigFlags, names []string, addresses map[string]string) error {
	c, err := f.node.loadCluster(ctx)
	if err != nil {
		return err
	}
	creds, err := a.loadSecretCredentials(ctx, f.node.secrets)
	if err != nil {
		return err
	}
	if err := c.checkAddresses(addresses); err != nil {
		return err
	}
	r := &configRollout{
		rebooter: &rebooter{a: a, cluster: c, reach: nodeSet{cluster: c, creds: creds, addresses: addresses}, rebootOptions: a.rebootOptions(f.rollout, rolloutQuorumWait)},
		f:        f,
		nodes:    map[string]*rolloutNode{},
	}
	if err := r.plan(ctx, names); err != nil {
		return err
	}
	order := names
	if len(r.ordered) > 0 {
		order = r.order()
	}
	for _, name := range order {
		delivered, err := r.node(ctx, r.nodes[name])
		if err != nil {
			r.summary(ctx)
			return err
		}
		if delivered && f.until == untilDeliver {
			return nil
		}
	}
	return nil
}

// plan reads, before anything is delivered, each node's layers and, from several, its status,
// and checks its config against the image it runs; then it asks the node's Rollout layers whether
// the config changes the versions they bound against the config the node runs. A run that changes
// none asks no layer's service anything. One that changes some reads each node's roles, and the
// layers that find a change check the versions the nodes of their clusters run.
func (r *configRollout) plan(ctx context.Context, names []string) error {
	for _, name := range names {
		if err := r.cluster.node(name); err != nil {
			return err
		}
		n := &rolloutNode{}
		n.name = name
		t, err := r.reach.target(name)
		if err != nil {
			return err
		}
		if err := callNode(t, func(conn *client.Conn) error {
			info, err := conn.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
			if err != nil {
				return err
			}
			n.layers = info.Msg.Layers
			// Several nodes are checked before the first gets anything; one node is checked by its
			// delivery, as before.
			if len(names) > 1 {
				st, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
				if err != nil {
					return err
				}
				if err := trialOpen(name, st.Msg); err != nil {
					return err
				}
				ct, err := nodeContract(ctx, conn, name)
				if err != nil {
					return err
				}
				d, err := r.cluster.deliveryFor(name, ct)
				if err != nil {
					return err
				}
				n.version, n.runs = d.Version, st.Msg.ConfigVersion
			}
			return r.changes(ctx, conn, n)
		}); err != nil {
			return err
		}
		r.nodes[name] = n
	}
	if len(r.ordered) == 0 {
		return nil
	}
	if r.f.payloads() {
		return fmt.Errorf("the config changes versions the cluster orders its nodes by, so the nodes that act for the others get it on trial, which %s does not go with; deliver it apart", r.f.payloadFlags)
	}
	probed := map[string]bool{}
	for _, name := range names {
		n := r.nodes[name]
		if err := r.call(name, func(conn *client.Conn) error {
			for _, u := range pluginsOf[layer.Upgrade](n.layers) {
				role, err := u.UpgradeRole(ctx, conn)
				if err != nil {
					return err
				}
				n.roles = append(n.roles, upgradeRole{u, role})
			}
			return nil
		}); err != nil {
			return err
		}
		probed[name] = n.roles.coordinator()
	}
	for _, l := range r.ordered {
		if err := l.CheckConfig(r, pluginCluster{r.cluster}, names, r.readVersions(ctx, l)); err != nil {
			return err
		}
	}
	return r.findCoordinators(ctx, probed, nil)
}

// changes asks the node's Rollout layers whether the inventory's config changes the versions they
// bound against the config the node runs, which it reads on conn, says what each changes, and adds
// the layers that find a change to the run's ordered ones.
func (r *configRollout) changes(ctx context.Context, conn *client.Conn, n *rolloutNode) error {
	layers := pluginsOf[layer.Rollout](n.layers)
	if len(layers) == 0 {
		return nil
	}
	running, err := slotConfig(ctx, conn, false)
	if err != nil {
		return err
	}
	for _, l := range layers {
		change, err := l.Changes(pluginCluster{r.cluster}, n.name, running)
		if err != nil {
			return err
		}
		if change == "" {
			continue
		}
		r.say("%s: the config changes %s", n.name, change)
		if !slices.Contains(r.ordered, l) {
			r.ordered = append(r.ordered, l)
		}
	}
	return nil
}

// order is the order of an ordered run: the coordinators by name, then the other nodes by name.
func (r *configRollout) order() []string {
	var coordinators, others []string
	for _, name := range slices.Sorted(maps.Keys(r.nodes)) {
		if r.coordinator(r.nodes[name]) {
			coordinators = append(coordinators, name)
		} else {
			others = append(others, name)
		}
	}
	return append(coordinators, others...)
}

// node delivers the config to the node and reports whether it delivered it. A node that runs the
// config already gets nothing; in an ordered run a coordinator is then checked as one the run
// delivered to, so a run again continues once it serves its cluster. A coordinator of an ordered
// run is held by its layers and gets the config on trial.
func (r *configRollout) node(ctx context.Context, n *rolloutNode) (bool, error) {
	t, err := r.reach.target(n.name)
	if err != nil {
		return false, err
	}
	coordinator := r.coordinator(n)
	if n.runs != "" && n.runs == n.version {
		r.say("%s runs config %s already", n.name, n.version)
		if coordinator {
			return false, r.a.waitDelivered(ctx, t, r.a.now().Add(rolloutTrial))
		}
		return false, nil
	}
	f := r.f
	if coordinator {
		for _, l := range r.ordered {
			if err := l.BeforeDelivery(ctx, r, n.name, n.roleIn(l)); err != nil {
				return false, err
			}
		}
	}
	if (coordinator || f.until == untilDeliver) && f.try == 0 {
		f.try = rolloutTrial
	}
	f.noConfirm = f.noConfirm || f.until == untilDeliver
	return true, r.a.deliverConfig(ctx, f, t)
}

// readVersions reads, for the Rollout layer l, the versions the nodes it names run, as
// layer.ReadVersions describes: only the nodes the layer names are reached, and a node chalkctl
// cannot reach is given with the error to l alone.
func (b *rebooter) readVersions(ctx context.Context, l layer.Rollout) layer.ReadVersions {
	return func(names []string) []layer.NodeVersions {
		if names == nil {
			names = b.nodeNames()
		}
		var all []layer.NodeVersions
		for _, name := range names {
			var v *layer.NodeVersions
			err := b.call(name, func(conn *client.Conn) error {
				info, err := conn.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
				if err != nil {
					return err
				}
				if slices.Contains(pluginsOf[layer.Rollout](info.Msg.Layers), l) {
					versions, err := l.Versions(ctx, conn)
					v = &layer.NodeVersions{Name: name, Versions: versions, Err: err}
				}
				return nil
			})
			switch {
			case err != nil:
				all = append(all, layer.NodeVersions{Name: name, Err: err})
			case v != nil:
				all = append(all, *v)
			}
		}
		return all
	}
}

// summary prints which nodes run which versions, as the ordered layers say it, once a run stopped.
func (r *configRollout) summary(ctx context.Context) {
	if len(r.ordered) == 0 {
		return
	}
	for _, l := range r.ordered {
		for _, line := range l.Summary(pluginCluster{r.cluster}, r.readVersions(ctx, l)) {
			r.say("%s", line)
		}
	}
}

// checkImages has the Rollout layers refuse images whose versions would break their bounds
// against what the nodes of their clusters run, before any node is sent its image. A layer reads
// the nodes' versions only when an image concerns a node of its cluster.
func (r *upgradeRun) checkImages(ctx context.Context) error {
	layers := compiled[layer.Rollout]()
	if len(layers) == 0 {
		return nil
	}
	images := map[string][]*anypb.Any{}
	for name, n := range r.nodes {
		if n.image != nil && n.image.contract != nil {
			images[name] = n.image.contract.facts
		}
	}
	for _, l := range layers {
		if err := l.CheckImages(pluginCluster{r.cluster}, images, r.readVersions(ctx, l)); err != nil {
			return err
		}
	}
	return nil
}

// waitDelivered waits, until the time given, for the Rollout layers of the image the node runs to
// find that it serves their clusters with the config it runs, asking over a new connection each
// time. The layers are read on each attempt too: a node that does not answer for a while after a
// delivery, as one whose config restarts its network, is asked again until the time given.
func (a *app) waitDelivered(ctx context.Context, t *target, until time.Time) error {
	said := false
	var last error
	err := poll.Until(ctx, "", until.Sub(a.now()), a.poll(), func() error {
		// An attempt ends with the wait, so a layer whose service does not answer holds the trial no
		// longer than its deadline.
		ctx, cancel := context.WithDeadline(ctx, until)
		defer cancel()
		err := callNode(t, func(conn *client.Conn) error {
			info, err := conn.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
			if err != nil {
				return err
			}
			layers := pluginsOf[layer.Rollout](info.Msg.Layers)
			if len(layers) > 0 && !said {
				fmt.Fprintf(a.stdout, "%s: waiting for its layers to serve with the config\n", t.name)
				said = true
			}
			for _, l := range layers {
				if err := l.Delivered(ctx, conn); err != nil {
					return err
				}
			}
			return nil
		})
		// An attempt the deadline cut short says less than the one before it.
		if err != nil && (last == nil || connect.CodeOf(err) != connect.CodeDeadlineExceeded) {
			last = err
		}
		return err
	})
	if err != nil && ctx.Err() == nil {
		return cmp.Or(last, err)
	}
	return err
}

// rollbackTrial rolls the node's config on trial back to config previous at once, after failure,
// rather than at the trial's deadline. When the rollback fails, it waits for the node to roll the
// config back by itself, as trialRolledBack does.
func (a *app) rollbackTrial(ctx context.Context, t *target, version, previous string, deadline time.Time, failure error) error {
	err := callNode(t, func(conn *client.Conn) error {
		_, err := conn.RollbackConfig(ctx, connect.NewRequest(&nodev1.RollbackConfigRequest{Version: previous}))
		return err
	})
	if err != nil {
		fmt.Fprintf(a.stderr, "chalkctl: warning: rolling config %s back failed: %v; waiting for the trial's deadline\n", version, err)
		return a.trialRolledBack(ctx, t, version, previous, deadline, failure)
	}
	fmt.Fprintf(a.stdout, "%s runs config %s again: config %s was rolled back\n", t.name, previous, version)
	return failure
}

// coordinator reports whether the node acts for the others in the cluster of a layer that orders
// the run; a node that does so in another layer's cluster only goes with the other nodes.
func (r *configRollout) coordinator(n *rolloutNode) bool {
	return slices.ContainsFunc(r.ordered, func(l layer.Rollout) bool { return n.roleIn(l).Coordinator })
}

// roleIn is the node's role in the cluster of the layer l, as the layer's Upgrade hooks read it;
// none when the layer reads no role.
func (n *rolloutNode) roleIn(l layer.Rollout) layer.UpgradeRole {
	for _, r := range n.roles {
		if p, ok := r.u.(layer.Plugin); ok && p == l.(layer.Plugin) {
			return r.role
		}
	}
	return layer.UpgradeRole{}
}

// checkChanges refuses an upgrade whose config, which each node gets with its image, changes a
// version a Rollout layer bounds, against the config the node runs: apply-config delivers such a
// change in the order the layer needs, which an upgrade does not. A run that withholds config
// delivers none, and a node that refuses to show its config has the run withhold it.
func (r *upgradeRun) checkChanges(ctx context.Context) error {
	layers := compiled[layer.Rollout]()
	if len(layers) == 0 {
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(r.nodes)) {
		if r.deliveryOf(r.nodes[name]) == nil {
			continue
		}
		var running []config.Document
		denied := false
		if err := r.call(name, func(conn *client.Conn) error {
			var err error
			running, err = slotConfig(ctx, conn, false)
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
			return nil
		}
		for _, l := range layers {
			change, err := l.Changes(pluginCluster{r.cluster}, name, running)
			if err != nil {
				return err
			}
			if change != "" {
				return fmt.Errorf("%s: the inventory's config changes %s, which an upgrade does not deliver; deliver the config with chalkctl apply-config first, which orders the nodes for it, then upgrade", name, change)
			}
		}
	}
	return nil
}
