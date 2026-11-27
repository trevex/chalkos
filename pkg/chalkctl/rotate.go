package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/sdk"
)

type rotateFlags struct {
	node                nodeFlags
	change              changeFlags
	resume, finish, now bool
	ignoreRevoked       bool
	timeout             time.Duration
}

func (a *app) rotateCommand() *cobra.Command {
	f := rotateFlags{node: nodeFlags{needs: "rotate"}}
	cmd := a.command(&cobra.Command{
		Use:         "rotate <kind>",
		Short:       "Rotate " + kindList(rotationKinds(), " or "),
		Long:        rotateLong,
		Example:     rotateExample,
		Args:        cli.ExactArgs(1),
		Annotations: layer.Meta{Credentials: layer.SecretsFile}.Annotations(),
	}, func(a *app, ctx context.Context, pos []string) error { return a.rotate(ctx, f, pos) })
	fs := cmd.Flags()
	f.node.Register(fs)
	f.change.register(fs)
	address := fs.Lookup("address")
	address.Value, address.Usage = namedAddresses{&f.node.Address}, namedAddressesUsage
	fs.BoolVar(&f.resume, "resume", false, "continue the rotation the secrets file records")
	fs.BoolVar(&f.finish, "finish", false, "remove the old value from every node and the secrets file")
	fs.BoolVar(&f.now, "now", false, "with --finish, finish now although the kind asks to wait, as the service-account key does within the hour after its switch")
	fs.BoolVar(&f.ignoreRevoked, "ignore-revoked", false, "rotate every node of the inventory, revoked ones included, as while no layer can say which are revoked, and refresh the revoked ones' revocations after each phase")
	fs.DurationVar(&f.timeout, "timeout", 10*time.Minute, "how long to wait for each node to apply a phase")
	return cmd
}

func (a *app) rotate(ctx context.Context, f rotateFlags, pos []string) error {
	sf, change := f.node.Secrets, f.change
	switch {
	case f.resume && f.finish:
		return cli.Usagef("pass --resume or --finish, not both")
	case f.now && !f.finish:
		return cli.Usagef("--now goes with --finish")
	case f.timeout <= 0:
		return cli.Usagef("--timeout must be positive")
	}
	kind := clustersecrets.RotationKind(pos[0])
	if !slices.Contains(clustersecrets.RotationKinds, kind) {
		return cli.Usagef("unknown kind %q; rotate one of %s", kind, kindList(rotationKinds(), ", "))
	}
	if rotatorOf(nil, kind) == nil {
		return cli.Usagef("no layer of chalkctl rotates the %s", kind.Name())
	}
	c, err := f.node.loadCluster(ctx)
	if err != nil {
		return err
	}
	addresses, err := f.node.addresses(c, "")
	if err != nil {
		return err
	}
	// Every node's config is read before any node is sent anything.
	for _, name := range c.NodeNames() {
		if _, err := c.config(name); err != nil {
			return err
		}
	}
	file, err := a.openSecrets(ctx, sf, change)
	if err != nil {
		return err
	}
	defer file.Close()
	r := &rotation{a: a, cluster: c, file: file, addresses: addresses, timeout: f.timeout, poll: a.poll(), ignoreRevoked: f.ignoreRevoked}
	if f.ignoreRevoked {
		fmt.Fprintln(a.stderr, "chalkctl: warning: --ignore-revoked: rotate skips no revoked node, so a revoked node still in the inventory "+
			"receives the new value and certificates of it like any other, and each phase then refreshes its revocation; remove revoked nodes from the inventory first")
	}
	// What an earlier run did not refresh is refreshed first, so the finish prunes after it.
	if err := r.refreshRevoked(ctx, nil); err != nil {
		fmt.Fprintf(a.stderr, "chalkctl: warning: %v\n", err)
	}
	if err := r.skipRevoked(ctx); err != nil {
		return err
	}
	switch {
	case f.finish:
		return r.finish(ctx, kind, f.now)
	case f.resume:
		return r.resume(ctx, kind)
	}
	return r.start(ctx, kind)
}

// rotation runs the phases of a rotation on the nodes of the cluster, recording each in the
// secrets file.
type rotation struct {
	a       *app
	cluster *cluster
	file    *secretsFile
	// addresses are the nodes' chalkd addresses that --address gives.
	addresses map[string]string
	// timeout bounds each wait for a node; poll is the time between two looks.
	timeout, poll time.Duration
	// revoked are the nodes the rotation skips, because a layer lists them as revoked. With
	// ignoreRevoked, no node is skipped, and the lists are read only to refresh the revocations.
	revoked       map[string]bool
	ignoreRevoked bool
	// unrefreshed is why the last refresh of the revocations failed, with --ignore-revoked.
	unrefreshed error
}

// Say prints a line of the rotation's progress.
func (r *rotation) Say(format string, args ...any) {
	fmt.Fprintf(r.a.stdout, format+"\n", args...)
}

// save changes a copy of the secrets and writes it, so the file and what chalkctl holds never
// differ.
func (r *rotation) save(change func(s *clustersecrets.Secrets) error) error {
	s := r.file.secrets
	if s.Rotation != nil {
		copied := *s.Rotation
		s.Rotation = &copied
	}
	if err := change(&s); err != nil {
		return err
	}
	return r.a.writeSecretsFile(r.file, s)
}

func (r *rotation) start(ctx context.Context, kind clustersecrets.RotationKind) error {
	if err := rotatorOf(r, kind).CheckStart(ctx, r, kind); err != nil {
		return err
	}
	if err := r.save(func(s *clustersecrets.Secrets) error { return s.BeginRotation(kind, time.Now()) }); err != nil {
		return err
	}
	r.Say("%s records the rotation of the %s; %s", r.file.out, kind.Name(), r.file.written())
	return r.run(ctx)
}

func (r *rotation) resume(ctx context.Context, kind clustersecrets.RotationKind) error {
	if err := r.running(kind); err != nil {
		return err
	}
	return r.run(ctx)
}

// running checks that the secrets file records a rotation of kind.
func (r *rotation) running(kind clustersecrets.RotationKind) error {
	switch rot := r.file.secrets.Rotation; {
	case rot == nil:
		return fmt.Errorf("%s records no rotation; start one with chalkctl rotate %s", r.file.path, kind)
	case rot.Kind != kind:
		return fmt.Errorf("%s records a rotation of the %s, not of the %s", r.file.path, rot.Kind.Name(), kind.Name())
	}
	return nil
}

func (r *rotation) finish(ctx context.Context, kind clustersecrets.RotationKind, now bool) error {
	if err := r.running(kind); err != nil {
		return err
	}
	rot := r.file.secrets.Rotation
	if rot.Phase != clustersecrets.PhaseFinish {
		if rot.Phase != clustersecrets.PhaseRefresh || !rot.Applied {
			return fmt.Errorf("the rotation of the %s is in its %s phase; finish it once its refresh phase is applied, after chalkctl rotate %s --resume", kind.Name(), rot.Phase, kind)
		}
		if err := rotatorOf(r, kind).FinishGuard(ctx, r, kind, now); err != nil {
			return err
		}
		if err := r.save(func(s *clustersecrets.Secrets) error { return s.FinishRotation() }); err != nil {
			return err
		}
	}
	return r.run(ctx)
}

// run applies the phase the secrets file records and the ones after it, up to the kind's pause
// or the end of the finish. It fails while the last refresh of the revocations failed.
func (r *rotation) run(ctx context.Context) error {
	return errors.Join(r.phases(ctx), r.unrefreshed)
}

// phases is run but for the refresh of the revocations.
func (r *rotation) phases(ctx context.Context) error {
	// rotate read the revoked nodes before the first phase; a node revoked while a phase ran is
	// left out of the phases after it.
	first := true
	for {
		rot := *r.file.secrets.Rotation
		rotator := rotatorOf(r, rot.Kind)
		if !rot.Applied {
			if !first {
				if err := r.skipRevoked(ctx); err != nil {
					return fmt.Errorf("the %s phase of the rotation of the %s did not start: %w; continue with chalkctl rotate %s --resume", rot.Phase, rot.Kind.Name(), err, rot.Kind)
				}
			}
			first = false
			var pending refreshes
			if r.ignoreRevoked {
				pending = r.revokedBefore(ctx)
			}
			r.Say("%s: the %s phase", rot.Kind, rot.Phase)
			err := rotator.ApplyRotation(ctx, r, rot.Kind, rot.Phase)
			// The phase handed out what it delivered to some nodes also when it stopped.
			if r.ignoreRevoked {
				r.unrefreshed = r.refreshRevoked(ctx, pending)
			}
			if err != nil {
				return fmt.Errorf("the %s phase of the rotation of the %s stopped: %w; once that is fixed, continue with chalkctl rotate %s --resume", rot.Phase, rot.Kind.Name(), err, rot.Kind)
			}
			if err := r.save(func(s *clustersecrets.Secrets) error { return s.RecordApplied(time.Now()) }); err != nil {
				return err
			}
			r.Say("%s: every node applied the %s phase", rot.Kind, rot.Phase)
			if rotator.PausesAfter(rot.Kind, rot.Phase) {
				return rotator.Paused(ctx, r, rot)
			}
		}
		var err error
		switch rot.Phase {
		case clustersecrets.PhaseAccept:
			err = r.save(func(s *clustersecrets.Secrets) error { return s.SwitchRotation() })
		case clustersecrets.PhaseSwitch:
			err = r.save(func(s *clustersecrets.Secrets) error { return s.RefreshRotation() })
		case clustersecrets.PhaseRefresh:
			if rotator.PausesAfter(rot.Kind, rot.Phase) {
				return rotator.Paused(ctx, r, rot)
			}
			// A kind that does not pause after its refresh finishes at once.
			if err := rotator.FinishGuard(ctx, r, rot.Kind, false); err != nil {
				return err
			}
			err = r.save(func(s *clustersecrets.Secrets) error { return s.FinishRotation() })
		case clustersecrets.PhaseFinish:
			if err := r.save(func(s *clustersecrets.Secrets) error { return s.EndRotation() }); err != nil {
				return err
			}
			state, advice := rotator.Finished(rot.Kind)
			r.Say("the rotation of the %s is finished: %s; %s", rot.Kind.Name(), state, r.file.written())
			if advice != "" {
				r.Say("%s", advice)
			}
			r.pruneRevoked(ctx, rot.Kind)
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// rotators are what runs each kind of rotation: chalkctl's own for what every node takes part in,
// then every compiled plugin's.
func rotators(r *rotation) []layer.Rotation {
	return append([]layer.Rotation{caRotation{r}}, compiled[layer.Rotation]()...)
}

// rotatorOf is what runs the rotation of kind; nil when nothing does. rotate refused a kind
// without one, and running a recorded kind other than the one named, so the rotation's own calls
// never get nil.
func rotatorOf(r *rotation, kind clustersecrets.RotationKind) layer.Rotation {
	for _, p := range rotators(r) {
		if slices.Contains(p.Kinds(), kind) {
			return p
		}
	}
	return nil
}

// rotationKinds are the kinds chalkctl rotate runs, in the order it names them.
func rotationKinds() []clustersecrets.RotationKind {
	var kinds []clustersecrets.RotationKind
	for _, p := range rotators(nil) {
		kinds = append(kinds, p.Kinds()...)
	}
	return kinds
}

// kindList names kinds as chalkctl rotate takes them, separated by commas and the last by last,
// such as " or ".
func kindList(kinds []clustersecrets.RotationKind, last string) string {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + last + names[len(names)-1]
}

// skipRevoked leaves the nodes the layers list as revoked out of the rotation and names each the
// first time. Without a layer's list no phase runs, so no revoked node gets a certificate. A
// skipped node stays skipped for the run, as it missed the earlier phases.
func (r *rotation) skipRevoked(ctx context.Context) error {
	if r.ignoreRevoked {
		return nil
	}
	nodes := r.allNodes()
	list := map[string]string{}
	for _, p := range compiled[layer.RotationNodes]() {
		revoked, err := p.RevokedNodes(ctx, r)
		if err != nil {
			return fmt.Errorf("%w; rotate runs only once one does, so it never hands a certificate to a revoked node, or with --ignore-revoked", err)
		}
		for name, since := range revoked {
			if _, ok := list[name]; !ok {
				list[name] = since
			}
		}
	}
	if r.revoked == nil {
		r.revoked = map[string]bool{}
	}
	for _, name := range slices.Sorted(maps.Keys(list)) {
		if slices.Contains(nodes, name) && !r.revoked[name] {
			r.revoked[name] = true
			r.Say("skipping %s: revoked %s", name, list[name])
		}
	}
	return nil
}

// revokedBefore reads, with --ignore-revoked, which of the nodes that take part the layers list as
// revoked, before a phase hands them what it delivers.
func (r *rotation) revokedBefore(ctx context.Context) refreshes {
	nodes, err := r.nodes()
	if err != nil {
		// The refresh after the phase fails the same way, and the run with it.
		return nil
	}
	return revokedBefore(ctx, nodes, r.NodeNames(), false)
}

// refreshRevoked refreshes pending, the revocations of the revoked nodes the phase that ran last
// handed what it delivers, so that pruning keeps them listed while it is valid, and those the
// secrets file records as not refreshed; it records which are left. Its error, while one is left,
// is the run's when it follows a phase: the refresh covers what every earlier phase handed out
// too, so it is the last refresh's that counts.
func (r *rotation) refreshRevoked(ctx context.Context, pending refreshes) error {
	recorded := r.file.secrets.Unrefreshed
	rs := pending.withRecorded(recorded)
	if len(rs) == 0 {
		return nil
	}
	nodes, err := r.nodes()
	if err != nil {
		return err
	}
	tried, left, err := r.a.refreshRevocations(ctx, nodes, rs)
	s := r.file.secrets
	if s.RecordRefreshes(tried, left) {
		if serr := r.save(func(s *clustersecrets.Secrets) error { s.RecordRefreshes(tried, left); return nil }); serr != nil {
			return errors.Join(err, serr)
		}
	}
	if err != nil {
		return fmt.Errorf("%w\n%s", err, recordedNote)
	}
	return nil
}

// pruneRevoked has every layer drop the revoked nodes the finished rotation of kind left nothing
// to let back in. A layer that fails to is warned of: the rotation is finished all the same.
// While the last refresh of the revocations failed, nothing is pruned: the revoked nodes it did
// not refresh hold what the rotation handed out.
func (r *rotation) pruneRevoked(ctx context.Context, kind clustersecrets.RotationKind) {
	if r.unrefreshed != nil {
		fmt.Fprintln(r.a.stderr, "chalkctl: warning: the revoked nodes were not pruned: their revocations were not refreshed, and pruning could drop those that hold what the rotation handed out")
		return
	}
	for _, p := range compiled[layer.RotationNodes]() {
		if err := p.PruneRevoked(ctx, r, kind); err != nil {
			fmt.Fprintf(r.a.stderr, "chalkctl: warning: the revoked nodes were not pruned: %v\n", err)
		}
	}
}

// NodeNames lists the cluster's nodes in name order, but the revoked ones.
func (r *rotation) NodeNames() []string {
	return slices.DeleteFunc(slices.Clone(r.cluster.NodeNames()), func(name string) bool { return r.revoked[name] })
}

// allNodes lists the nodes that take part, the coordinators first, each group in name order.
func (r *rotation) allNodes() []string {
	return coordinatorsFirst(r.NodeNames(), func(name string) bool {
		// rotate read every node's config before it started, so no error is left to report.
		ok, _ := listedCoordinator(r.cluster, name)
		return ok
	})
}

// nodes reaches the nodes with the secrets as they are now: chalkctl's admin certificate comes
// from the OS CA that issues, and it trusts every OS CA accepted.
func (r *rotation) nodes() (nodeSet, error) {
	creds, err := secretCredentials(r.file.secrets)
	if err != nil {
		return nodeSet{}, err
	}
	return nodeSet{cluster: r.cluster, creds: creds, addresses: r.addresses, release: r.a.releases}, nil
}

// Call runs fn against a node.
func (r *rotation) Call(name string, fn func(conn *client.Conn) error) error {
	nodes, err := r.nodes()
	if err != nil {
		return err
	}
	return nodes.Call(name, fn)
}

// deliver sends a node what req carries.
func (r *rotation) deliver(ctx context.Context, name string, req *nodev1.ApplyConfigRequest) error {
	return r.Call(name, func(conn *client.Conn) error {
		resp, err := conn.ApplyConfig(client.Unbounded(ctx), req)
		if err != nil {
			return err
		}
		r.a.warnDelivery(name, resp)
		return nil
	})
}

// DeliverPayloads sends a node the payloads its layers issue it as the secrets file has them now.
func (r *rotation) DeliverPayloads(ctx context.Context, name string) error {
	nodes, err := r.nodes()
	if err != nil {
		return err
	}
	t, err := nodes.target(name)
	if err != nil {
		return err
	}
	docs, err := t.cluster.Documents(name)
	if err != nil {
		return err
	}
	payloads, err := sdk.PayloadsOf(sdkLayers(), t.secrets, name, docs, time.Now())
	if err != nil {
		return err
	}
	return r.deliver(ctx, name, &nodev1.ApplyConfigRequest{Layers: payloads})
}

// Status reads a node's status.
func (r *rotation) Status(ctx context.Context, name string) (*nodev1.StatusResponse, error) {
	nodes, err := r.nodes()
	if err != nil {
		return nil, err
	}
	return nodes.status(ctx, name)
}

var _ layer.Rotating = (*rotation)(nil)

func (r *rotation) Secrets() clustersecrets.Secrets { return r.file.secrets }
func (r *rotation) Timeout() time.Duration          { return r.timeout }
func (r *rotation) Poll() time.Duration             { return r.poll }
func (r *rotation) Cluster() layer.Cluster          { return r.cluster }

func (r *rotation) Nodes() (layer.Nodes, error) {
	nodes, err := r.nodes()
	if err != nil {
		return nil, err
	}
	return nodes, nil
}

// publicFile names the public half of the secrets file.
func (r *rotation) publicFile() string {
	if r.file.publicOut != "" {
		return r.file.publicOut
	}
	return "secrets.pub.json"
}
