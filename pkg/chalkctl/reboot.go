package chalkctl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/internal/poll"
)

// rebooter reboots nodes of a cluster as chalkctl upgrade and chalkctl node reboot both do,
// one node at a time: the node's layers hold it (Kubernetes keeps etcd's quorum and drains
// it), it reboots, chalkctl waits until it runs the image it boots next and its boot was
// found healthy, and its layers release it once it serves their clusters again (Kubernetes
// uncordons it once its Node is Ready). The layers act through the cluster's coordinators,
// such as Kubernetes control planes.
type rebooter struct {
	a       *app
	cluster *cluster
	// reach reaches the nodes with the run's credentials, at the addresses --address gives.
	reach nodeSet
	rebootOptions
	// coordinators are the cluster's nodes that act for the others, which the layers drain and
	// release nodes through.
	coordinators []string
	// fellBack is the error of a node that fell back from the image it rebooted into, with what its
	// boots logged; nil says so of a reboot alone.
	fellBack func(n *rebootNode, boot *nodev1.BootStatus) error
	out      sync.Mutex
}

// rebootOptions are how a run holds and waits for the nodes it reboots, as its flags give it.
type rebootOptions struct {
	// layerFlags are the values of the layers' flags, by layer, as their UpgradeFlags returned them.
	layerFlags                              map[string]any
	timeout, drainTimeout, poll, quorumWait time.Duration
	// grace is how long a node's boot may stay unhealthy after it settled before the wait for it
	// stops: a check of the layers, such as etcd's or the kubelet's, may fail once while the
	// node serves.
	grace time.Duration
}

// rebootOptions are the options of a run whose wait for each node and each drain take timeout
// at most.
func (a *app) rebootOptions(layerFlags map[string]any, timeout time.Duration) rebootOptions {
	grace := time.Minute
	if a.unhealthyGrace > 0 {
		grace = a.unhealthyGrace
	}
	return rebootOptions{layerFlags: layerFlags, timeout: timeout, drainTimeout: timeout, poll: a.poll(), quorumWait: min(timeout, time.Minute), grace: grace}
}

// rebootNode is a node the reboot step reboots, by its roles in the clusters of its layers.
type rebootNode struct {
	name  string
	roles upgradeRoles
	// uncounted is set once a rollback staged its image on the node without tries, so systemd-boot
	// does not count its boots and none is found healthy.
	uncounted bool
	// held is set once the node's layers held it before its reboot, so a node that falls back stays
	// held.
	held bool
}

// upgradeRole is a node's role in the cluster of one of its layers, with the layer's hooks.
type upgradeRole struct {
	u    layer.Upgrade
	role layer.UpgradeRole
}

// upgradeRoles are a node's roles in the clusters of its layers.
type upgradeRoles []upgradeRole

// coordinator reports whether the node acts for the others in one of its layers.
func (rs upgradeRoles) coordinator() bool {
	return slices.ContainsFunc(rs, func(r upgradeRole) bool { return r.role.Coordinator })
}

// member reports whether the node serves the cluster of one of its layers.
func (rs upgradeRoles) member() bool {
	return slices.ContainsFunc(rs, func(r upgradeRole) bool { return r.role.Member })
}

// bootTarget is the boot the reboot step waits for: of the image named image in messages, whose
// store has the root hash root, and other than the boot of the ID before.
type bootTarget struct {
	image string
	root  []byte
	// before is the boot ID the node's status gave before its reboot, which a boot of the same
	// image is told from; empty for a node that booted already, as one an earlier run rebooted, or
	// whose chalkd cannot read the ID.
	before string
}

// bootOf is the boot target of the node's next boot into the image, as its status st gave the
// boot before.
func bootOf(image string, root []byte, st *nodev1.StatusResponse) bootTarget {
	return bootTarget{image: image, root: root, before: st.GetBoot().GetBootId()}
}

// booted reports whether the boot of a node's status is the one waited for: of the target's image,
// from an entry that is blessed, and, as far as the status tells, other than the boot before.
func (t bootTarget) booted(boot *nodev1.BootStatus) bool {
	return bytes.Equal(boot.GetRunning().GetRootHash(), t.root) && boot.GetBlessed() && (t.before == "" || boot.GetBootId() != t.before)
}

// reboot runs the reboot step for the node: its layers hold it unless hold is false, it reboots
// into the target's image, and release waits for that boot and releases the node. activate, when
// set, runs once the node is held, right before the reboot: an upgrade makes the image boot next
// only then, so a refused hold, as when etcd would lose its quorum, leaves the image staged, which
// no reboot of the node boots meanwhile. An activation that fails leaves the image staged too,
// and the node, which does not reboot, is released at once, unless the image boots next all the
// same.
func (b *rebooter) reboot(ctx context.Context, n *rebootNode, t bootTarget, hold bool, activate func() error) error {
	if hold {
		n.held = true
		if err := b.hooks(ctx, n, layer.Upgrade.BeforeReboot); err != nil {
			return err
		}
	}
	if activate != nil {
		if err := activate(); err != nil {
			if !hold {
				return err
			}
			// An activation whose answer was lost, or that failed after its rename, may have made
			// the image boot next: released, the node would boot it at a reboot nothing holds it
			// for. It stays held then, and while its status cannot tell.
			if err := b.heldForActivation(ctx, n, t, err); err != nil {
				return err
			}
			if rerr := b.hooks(ctx, n, layer.Upgrade.After); rerr != nil {
				msg := "releasing it failed"
				if n.roles.member() {
					msg = "it stays cordoned, as releasing it failed"
				}
				return fmt.Errorf("%w; %s: %w", err, msg, rerr)
			}
			n.held = false
			return err
		}
	}
	if err := b.reach.Call(n.name, func(conn *client.Conn) error {
		_, err := conn.Reboot(ctx, connect.NewRequest(&nodev1.RebootRequest{}))
		return err
	}); err != nil {
		return err
	}
	b.say("%s: rebooting into %s", n.name, t.image)
	return b.release(ctx, n, t, hold)
}

// heldForActivation is the error of a node held for a reboot whose activation failed with err,
// when the node stays held: the target's image boots next all the same, or the node's status,
// which tells, cannot be read. It is nil when the image does not boot next, so the node is
// released.
func (b *rebooter) heldForActivation(ctx context.Context, n *rebootNode, t bootTarget, err error) error {
	held := "it stays held"
	if n.roles.member() {
		held = "it stays cordoned"
	}
	st, serr := b.reach.status(ctx, n.name)
	if serr != nil {
		return fmt.Errorf("%w; %s, as its status, which tells whether %s boots next, cannot be read: %w", err, held, t.image, serr)
	}
	if boot := st.GetBoot(); boot.GetStagedState() == nodev1.SlotState_SLOT_STATE_ACTIVE && bytes.Equal(boot.GetStaged().GetRootHash(), t.root) {
		return fmt.Errorf("%w; %s boots next all the same, so %s: chalkctl node reboot %s boots it and releases the node", err, t.image, held, n.name)
	}
	return nil
}

// release waits until the node runs the target's image in another boot than the one before, its
// boot was found healthy, and it serves the clusters of its layers again, then has its layers
// release it, unless held is false.
func (b *rebooter) release(ctx context.Context, n *rebootNode, t bootTarget, held bool) error {
	if err := b.waitHealthy(ctx, n, t); err != nil {
		return err
	}
	if !held {
		return nil
	}
	return b.hooks(ctx, n, layer.Upgrade.After)
}

// waitHealthy waits until the node runs the target's image in another boot than the one before,
// its boot was found healthy, and it serves the clusters of its layers again, or until it fell
// back or its boot settled and stayed unhealthy for the grace period. The chalkd of the boot
// before may answer until it stops, so without the ID of that boot a settled boot that is not
// healthy may be the one before, and only the timeout ends the wait.
func (b *rebooter) waitHealthy(ctx context.Context, n *rebootNode, t bootTarget) error {
	var fellBack, unhealthy error
	// checkHealth is set once a look found the boot the wait is for; only then is the node asked
	// to check its health, which runs the checks of its layers.
	var checkHealth bool
	// unhealthySince is when the looks since began to find the boot settled and unhealthy; zero
	// once one found it otherwise. A look that reached no node changes nothing.
	var unhealthySince time.Time
	err := poll.Until(ctx, "", b.timeout, b.poll, func() error {
		var st *nodev1.StatusResponse
		var notReady error
		err := b.reach.Call(n.name, func(conn *client.Conn) error {
			status := func() error {
				s, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{CheckHealth: checkHealth}))
				if err == nil {
					st = s.Msg
				}
				return err
			}
			if err := status(); err != nil {
				return err
			}
			if !checkHealth && t.booted(st.GetBoot()) {
				checkHealth = true
				if err := status(); err != nil {
					return err
				}
			}
			// The node checks this boot's health as its boot's health check does: blessed tells of
			// the entry, which an uncounted boot, as of an entry blessed before, finds blessed from
			// the start.
			if boot := st.GetBoot(); t.booted(boot) && boot.GetHealth().GetHealthy() {
				notReady = n.ready(ctx, conn)
			}
			return nil
		})
		if err != nil {
			return err
		}
		boot := st.GetBoot()
		health := boot.GetHealth()
		if settledUnhealthy := health.GetSettled() && !health.GetHealthy(); !settledUnhealthy {
			unhealthySince = time.Time{}
		}
		switch {
		case t.before != "" && boot.GetBootId() == t.before:
			return errors.New("it did not reboot yet")
		case bytes.Equal(boot.GetFailed().GetRootHash(), t.root):
			fellBack = b.failed(n, boot)
			return poll.Permanent{Err: fellBack}
		case !bytes.Equal(boot.GetRunning().GetRootHash(), t.root):
			return fmt.Errorf("it runs %s", bootImageName(boot.GetRunning()))
		case !boot.GetBlessed():
			return errors.New("its boot was not found healthy yet")
		case health == nil:
			return errors.New("it reports no health of its boot")
		case !health.GetHealthy() && health.GetSettled():
			// The boot loader counts a boot that settled no more, and the node gave its layers the
			// time a counted boot has, so waiting changes little. A check of a layer may still fail
			// once, as when etcd answers late, so the boot must stay unhealthy for the grace
			// period. A boot that may be the one before ends no wait.
			if unhealthySince.IsZero() {
				unhealthySince = time.Now()
			}
			if t.before != "" && time.Since(unhealthySince) >= b.grace {
				unhealthy = unhealthyError(n, t, health.GetError())
				return poll.Permanent{Err: unhealthy}
			}
			return fmt.Errorf("its boot settled and is not healthy: %s", health.GetError())
		case !health.GetHealthy():
			return fmt.Errorf("its boot is not healthy yet: %s", health.GetError())
		}
		return notReady
	})
	switch {
	case fellBack != nil:
		return fellBack
	case unhealthy != nil:
		return unhealthy
	case err != nil:
		return fmt.Errorf("%s: waiting for %s to be found healthy: %w", n.name, t.image, err)
	}
	if n.uncounted {
		// The entry is blessed from the start, so the boot loader counts none of its boots: only
		// the node's check found this one healthy.
		b.say("%s: runs %s, found healthy; its boots are not counted", n.name, t.image)
		return nil
	}
	b.say("%s: runs %s, found healthy", n.name, t.image)
	return nil
}

// failed is the error of a node that fell back from the image it rebooted into, as fellBack says
// it, with what its boots logged. A node its layers held stays held, for an operator to look at.
func (b *rebooter) failed(n *rebootNode, boot *nodev1.BootStatus) error {
	if b.fellBack != nil {
		return b.fellBack(n, boot)
	}
	return fellBackError(n, fmt.Sprintf("%s: its boot of %s failed: it fell back to %s", n.name, bootImageName(boot.Failed), bootImageName(boot.Running)), boot)
}

// unhealthyError is the error of a node whose boot of the target's image settled without being
// found healthy, for why, with whether it stays held.
func unhealthyError(n *rebootNode, t bootTarget, why string) error {
	msg := fmt.Sprintf("%s: its boot of %s settled and is not healthy: %s", n.name, t.image, why)
	if n.roles.member() && n.held {
		msg += "; it stays cordoned"
	}
	return errors.New(msg)
}

// fellBackError is the error msg says of a node that fell back, with whether it stays held and
// what its boots logged.
func fellBackError(n *rebootNode, msg string, boot *nodev1.BootStatus) error {
	if n.roles.member() && n.held {
		msg += "; it stays cordoned"
	}
	if len(boot.Journal) > 0 {
		msg += "; its boots logged:\n  " + strings.Join(boot.Journal, "\n  ")
	}
	return errors.New(msg)
}

// ready checks, on conn, that the node serves the cluster of each of its layers again.
func (n *rebootNode) ready(ctx context.Context, conn *client.Conn) error {
	for _, r := range n.roles {
		if err := r.u.Ready(ctx, conn, r.role); err != nil {
			return layer.Named(n.name, err)
		}
	}
	return nil
}

// noCoordinator is the error of the first of the node's layers that needs a coordinator for it,
// a member; nil when none does.
func (n *rebootNode) noCoordinator(name string) error {
	for _, r := range n.roles {
		if r.role.Member {
			return r.u.NoCoordinator(name)
		}
	}
	return nil
}

// hooks runs a hook of each of the node's layers, in their order.
func (b *rebooter) hooks(ctx context.Context, n *rebootNode, hook func(layer.Upgrade, context.Context, layer.Upgrading, string, layer.UpgradeRole) error) error {
	for _, role := range n.roles {
		if err := hook(role.u, ctx, b, n.name, role.role); err != nil {
			return err
		}
	}
	return nil
}

// say prints a line of the run's progress, from any node's goroutine.
func (b *rebooter) say(format string, args ...any) {
	b.out.Lock()
	defer b.out.Unlock()
	fmt.Fprintf(b.a.stdout, format+"\n", args...)
}

// probe reads what a node runs, its status and its roles in the clusters of its layers.
func (b *rebooter) probe(ctx context.Context, name string) (*nodev1.InfoResponse, *nodev1.StatusResponse, upgradeRoles, error) {
	var info *nodev1.InfoResponse
	var st *nodev1.StatusResponse
	var roles upgradeRoles
	err := b.reach.Call(name, func(conn *client.Conn) error {
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

// nodeNames are the cluster's nodes: the inventory's, or without one those --address names.
func (b *rebooter) nodeNames() []string {
	if b.cluster.Partial() {
		return slices.Sorted(maps.Keys(b.reach.addresses))
	}
	return b.cluster.NodeNames()
}

// findCoordinators names the cluster's coordinators: the nodes probed, or asked now, that act for
// the others, and those the inventory lists. The coordinators drain the nodes and keep their
// cluster's quorum, so every node's role is read, also of nodes the run leaves alone. Loading the
// inventory read every node's files already, so a node whose files are broken stops every command,
// and this adds only a node whose files hold the same document twice. Without an inventory, the
// nodes --address names that the run leaves alone are asked. When the run holds nodes, a node of
// held that its layers hold through a coordinator stops the run while the cluster has none.
func (b *rebooter) findCoordinators(ctx context.Context, probed map[string]bool, held []*rebootNode) error {
	for _, name := range b.nodeNames() {
		if _, ok := probed[name]; b.cluster.Partial() && !ok {
			_, _, roles, err := b.probe(ctx, name)
			if err != nil {
				return err
			}
			probed[name] = roles.coordinator()
		}
		listed, err := listedCoordinator(b.cluster, name)
		if err != nil {
			return err
		}
		if probed[name] || listed {
			b.coordinators = append(b.coordinators, name)
		}
	}
	if len(b.coordinators) == 0 {
		for _, n := range held {
			if err := n.noCoordinator(n.name); err != nil {
				return err
			}
		}
	}
	return nil
}

var _ layer.Upgrading = (*rebooter)(nil)

func (b *rebooter) Say(format string, args ...any) { b.say(format, args...) }
func (b *rebooter) Flags(layer string) any         { return b.layerFlags[layer] }
func (b *rebooter) DrainTimeout() time.Duration    { return b.drainTimeout }
func (b *rebooter) QuorumWait() time.Duration      { return b.quorumWait }
func (b *rebooter) Poll() time.Duration            { return b.poll }

// ViaCoordinators calls fn on a coordinator: the node itself first when it is one, then the
// others, moving on past those whose error next accepts.
func (b *rebooter) ViaCoordinators(prefer string, next func(error) bool, fn func(conn *client.Conn) error) error {
	names := slices.Clone(b.coordinators)
	if i := slices.Index(names, prefer); i > 0 {
		names = append([]string{prefer}, slices.Delete(names, i, i+1)...)
	}
	if len(names) == 0 {
		return layer.ErrNoCoordinator
	}
	return b.reach.ViaFirst(names, next, fn)
}

type nodeRebootFlags struct {
	node    nodeFlags
	pin     pinFlags
	noDrain bool
	layers  map[string]any
	timeout time.Duration
}

func (a *app) nodeRebootCommand() *cobra.Command {
	f := nodeRebootFlags{layers: map[string]any{}}
	cmd := a.command(&cobra.Command{
		Use:     "reboot <node>",
		Short:   "Reboot a node, held by its layers, and wait until it is healthy again",
		Long:    nodeRebootLong,
		Example: nodeRebootExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.nodeReboot(ctx, f, pos[0]) })
	fs := cmd.Flags()
	f.node.RegisterClient(fs)
	setAddressUsage(fs, "address of the node's chalkd, host or host:port, or NODE=ADDR of another node of the cluster, such as a control plane the drain goes through; repeat it for several (default each node's address in the inventory)")
	f.pin.register(fs)
	fs.BoolVar(&f.noDrain, "no-drain", false, "reboot the node without its layers holding it before and releasing it after, such as without draining a Kubernetes node")
	fs.DurationVar(&f.timeout, "timeout", 30*time.Minute, "how long to wait for the node to come back healthy and for its drain")
	for _, p := range plugins {
		if u, ok := p.(layer.UpgradeFlags); ok {
			f.layers[p.Name()] = u.UpgradeFlags(fs)
		}
	}
	return cmd
}

// nodeReboot reboots an installed node through the reboot step upgrade runs, into the image it
// boots next: the one an upgrade or a rollback activated, or else the one it runs. A node in
// maintenance mode, reached with --fingerprint or --insecure, reboots without more.
func (a *app) nodeReboot(ctx context.Context, f nodeRebootFlags, name string) error {
	if f.timeout <= 0 {
		return cli.Usagef("--timeout must be positive")
	}
	s, err := a.reach(ctx, f.node, name, true)
	if err != nil {
		return err
	}
	if f.pin.set() {
		t, err := s.target(name)
		if err != nil {
			return err
		}
		conn, err := a.dialEither(t, f.pin)
		if err != nil {
			return err
		}
		defer conn.Close()
		if _, err := conn.Reboot(ctx, connect.NewRequest(&nodev1.RebootRequest{})); err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "%s reboots\n", t.name)
		return nil
	}
	c := s.cluster
	// Below the operator role the node refuses the drain or the reboot, naming the role.
	b := &rebooter{a: a, cluster: c, reach: s, rebootOptions: a.rebootOptions(f.layers, f.timeout)}
	if err := c.node(name); err != nil {
		return err
	}
	_, st, roles, err := b.probe(ctx, name)
	if err != nil {
		return err
	}
	boot := st.GetBoot()
	if boot.GetError() != "" {
		return fmt.Errorf("%s: its boot is unknown: %s", name, boot.GetError())
	}
	next := boot.GetRunning()
	if boot.GetStagedState() == nodev1.SlotState_SLOT_STATE_ACTIVE {
		next = boot.GetStaged()
	}
	n := &rebootNode{name: name, roles: roles}
	if !f.noDrain {
		if err := b.findCoordinators(ctx, map[string]bool{name: roles.coordinator()}, []*rebootNode{n}); err != nil {
			return err
		}
	}
	return b.reboot(ctx, n, bootOf(bootImageName(next), next.GetRootHash(), st), !f.noDrain, nil)
}
