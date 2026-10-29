package chalkctl

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/internal/cli"
)

type rotateFlags struct {
	cluster               clusterFlags
	secrets               secretFlags
	change                changeFlags
	addresses             addressList
	resume, finish, force bool
	ignoreRevoked         bool
	timeout               time.Duration
}

func (a *app) rotateCommand() *cobra.Command {
	f := rotateFlags{addresses: addressList{}}
	cmd := a.command(&cobra.Command{
		Use:     "rotate <kind>",
		Short:   "Rotate " + kindList(rotationKinds(), " or "),
		Long:    rotateLong,
		Example: rotateExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.rotate(ctx, f, pos) })
	fs := cmd.Flags()
	f.cluster.register(fs)
	f.secrets.register(fs)
	f.change.register(fs)
	registerAddresses(fs, f.addresses)
	fs.BoolVar(&f.resume, "resume", false, "continue the rotation the secrets file records")
	fs.BoolVar(&f.finish, "finish", false, "remove the old value from every node and the secrets file")
	fs.BoolVar(&f.force, "force", false, "with --finish, finish although the kind asks to wait, as the service-account key does within the hour after its switch")
	fs.BoolVar(&f.ignoreRevoked, "ignore-revoked", false, "rotate every node of the inventory, revoked ones included, without reading the revoked nodes, as while no layer can say which are")
	fs.DurationVar(&f.timeout, "timeout", 10*time.Minute, "how long to wait for each node to apply a phase")
	return cmd
}

func (a *app) rotate(ctx context.Context, f rotateFlags, pos []string) error {
	cf, sf, change, addresses := f.cluster, f.secrets, f.change, f.addresses
	switch {
	case f.resume && f.finish:
		return cli.Usagef("pass --resume or --finish, not both")
	case f.force && !f.finish:
		return cli.Usagef("--force goes with --finish")
	}
	kind := clustersecrets.RotationKind(pos[0])
	if !slices.Contains(clustersecrets.RotationKinds, kind) {
		return cli.Usagef("unknown kind %q; rotate one of %s", kind, kindList(rotationKinds(), ", "))
	}
	if rotatorOf(nil, kind) == nil {
		return cli.Usagef("no layer of chalkctl rotates the %s", kind.Name())
	}
	c, err := loadCluster(ctx, cf)
	if err != nil {
		return err
	}
	if err := c.requireNodes("rotate"); err != nil {
		return err
	}
	if err := c.checkAddresses(addresses); err != nil {
		return err
	}
	// Every node's config is read before any node is sent anything.
	for _, name := range c.nodeNames() {
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
		fmt.Fprintln(a.stderr, "chalkctl: warning: --ignore-revoked: rotate reads no revoked nodes, so a revoked node still in the inventory "+
			"receives the new value and certificates of it like any other; remove revoked nodes from the inventory first")
	}
	if err := r.skipRevoked(ctx); err != nil {
		return err
	}
	switch {
	case f.finish:
		return r.finish(ctx, kind, f.force)
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
	// ignoreRevoked, the lists are not read and no node is skipped.
	revoked       map[string]bool
	ignoreRevoked bool
}

// say prints a line of the rotation's progress.
func (r *rotation) say(format string, args ...any) {
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
	if err := r.rotator(kind).CheckStart(ctx, r, kind); err != nil {
		return err
	}
	if err := r.save(func(s *clustersecrets.Secrets) error { return s.BeginRotation(kind, r.a.now()) }); err != nil {
		return err
	}
	r.say("%s records the rotation of the %s; %s", r.file.out, kind.Name(), r.file.written())
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

func (r *rotation) finish(ctx context.Context, kind clustersecrets.RotationKind, force bool) error {
	if err := r.running(kind); err != nil {
		return err
	}
	rot := r.file.secrets.Rotation
	if rot.Phase != clustersecrets.PhaseFinish {
		if rot.Phase != clustersecrets.PhaseRefresh || !rot.Applied {
			return fmt.Errorf("the rotation of the %s is in its %s phase; finish it once its refresh phase is applied, after chalkctl rotate %s --resume", kind.Name(), rot.Phase, kind)
		}
		if err := r.finishGuard(ctx, kind, force); err != nil {
			return err
		}
		if err := r.save(func(s *clustersecrets.Secrets) error { return s.FinishRotation() }); err != nil {
			return err
		}
	}
	return r.run(ctx)
}

// finishGuard refuses to remove an old value something still needs.
func (r *rotation) finishGuard(ctx context.Context, kind clustersecrets.RotationKind, force bool) error {
	return r.rotator(kind).FinishGuard(ctx, r, kind, force)
}

// run applies the phase the secrets file records and the ones after it, up to the kind's pause
// or the end of the finish.
func (r *rotation) run(ctx context.Context) error {
	// rotate read the revoked nodes before the first phase; a node revoked while a phase ran is
	// left out of the phases after it.
	first := true
	for {
		rot := *r.file.secrets.Rotation
		if !rot.Applied {
			if !first {
				if err := r.skipRevoked(ctx); err != nil {
					return fmt.Errorf("the %s phase of the rotation of the %s did not start: %w; continue with chalkctl rotate %s --resume", rot.Phase, rot.Kind.Name(), err, rot.Kind)
				}
			}
			first = false
			r.say("%s: the %s phase", rot.Kind, rot.Phase)
			if err := r.apply(ctx, rot.Kind, rot.Phase); err != nil {
				return fmt.Errorf("the %s phase of the rotation of the %s stopped: %w; once that is fixed, continue with chalkctl rotate %s --resume", rot.Phase, rot.Kind.Name(), err, rot.Kind)
			}
			if err := r.save(func(s *clustersecrets.Secrets) error { return s.RecordApplied(r.a.now()) }); err != nil {
				return err
			}
			r.say("%s: every node applied the %s phase", rot.Kind, rot.Phase)
			if r.rotator(rot.Kind).PausesAfter(rot.Kind, rot.Phase) {
				return r.paused(ctx, rot)
			}
		}
		var err error
		switch rot.Phase {
		case clustersecrets.PhaseAccept:
			err = r.save(func(s *clustersecrets.Secrets) error { return s.SwitchRotation() })
		case clustersecrets.PhaseSwitch:
			err = r.save(func(s *clustersecrets.Secrets) error { return s.RefreshRotation() })
		case clustersecrets.PhaseRefresh:
			if r.rotator(rot.Kind).PausesAfter(rot.Kind, rot.Phase) {
				return r.paused(ctx, rot)
			}
			// A kind that does not pause after its refresh finishes at once.
			if err := r.finishGuard(ctx, rot.Kind, false); err != nil {
				return err
			}
			err = r.save(func(s *clustersecrets.Secrets) error { return s.FinishRotation() })
		case clustersecrets.PhaseFinish:
			if err := r.save(func(s *clustersecrets.Secrets) error { return s.EndRotation() }); err != nil {
				return err
			}
			state, advice := r.rotator(rot.Kind).Finished(rot.Kind)
			r.say("the rotation of the %s is finished: %s; %s", rot.Kind.Name(), state, r.file.written())
			if advice != "" {
				r.say("%s", advice)
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// apply delivers a phase to the nodes and waits until each confirms it.
func (r *rotation) apply(ctx context.Context, kind clustersecrets.RotationKind, phase clustersecrets.Phase) error {
	return r.rotator(kind).ApplyRotation(ctx, r, kind, phase)
}

// paused says what the operator does before the rotation continues.
func (r *rotation) paused(ctx context.Context, rot clustersecrets.Rotation) error {
	return r.rotator(rot.Kind).Paused(ctx, r, rot)
}

// rotators are what runs each kind of rotation: chalkctl's own for what every node takes part in,
// then every compiled plugin's.
func rotators(r *rotation) []layer.Rotation {
	return append([]layer.Rotation{caRotation{r}}, compiled[layer.Rotation]()...)
}

// rotatorOf is what runs the rotation of kind; nil when nothing does.
func rotatorOf(r *rotation, kind clustersecrets.RotationKind) layer.Rotation {
	for _, p := range rotators(r) {
		if slices.Contains(p.Kinds(), kind) {
			return p
		}
	}
	return nil
}

// rotator is what runs the rotation of kind. rotate refused a kind without one, and running a
// recorded kind other than the one named.
func (r *rotation) rotator(kind clustersecrets.RotationKind) layer.Rotation {
	return rotatorOf(r, kind)
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

// skipRevoked reads the revoked nodes from every layer that lists them and leaves those of the
// cluster out of the rotation, naming each as it first skips it. Without a layer's list no phase
// runs, so none hands a certificate to a revoked node. A node stays skipped for the rest of the
// run once it is: it missed the phases before.
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
			r.say("skipping %s: revoked %s", name, list[name])
		}
	}
	return nil
}

// nodeNames lists the cluster's nodes in name order, but the revoked ones.
func (r *rotation) nodeNames() []string {
	return slices.DeleteFunc(slices.Clone(r.cluster.nodeNames()), func(name string) bool { return r.revoked[name] })
}

// allNodes lists the nodes that take part, the coordinators first, each group in name order.
func (r *rotation) allNodes() []string {
	var coordinators, others []string
	for _, name := range r.nodeNames() {
		// rotate read every node's config before it started, so no error is left to report.
		if ok, _ := listedCoordinator(r.cluster, name); ok {
			coordinators = append(coordinators, name)
		} else {
			others = append(others, name)
		}
	}
	return append(coordinators, others...)
}

// nodes reaches the nodes with the secrets as they are now: chalkctl's admin certificate comes
// from the OS CA that issues, and it trusts every OS CA accepted.
func (r *rotation) nodes() (nodeSet, error) {
	creds, err := secretCredentials(r.file.secrets)
	if err != nil {
		return nodeSet{}, err
	}
	return nodeSet{cluster: r.cluster, creds: creds, addresses: r.addresses}, nil
}

// target addresses a node with the secrets as they are now.
func (r *rotation) target(name string) (*target, error) {
	nodes, err := r.nodes()
	if err != nil {
		return nil, err
	}
	return nodes.target(name)
}

// call runs fn against a node.
func (r *rotation) call(name string, fn func(conn *client.Conn) error) error {
	nodes, err := r.nodes()
	if err != nil {
		return err
	}
	return nodes.call(name, fn)
}

// deliver sends a node what req carries.
func (r *rotation) deliver(ctx context.Context, name string, req *nodev1.ApplyConfigRequest) error {
	return r.call(name, func(conn *client.Conn) error {
		resp, err := conn.ApplyConfig(ctx, connect.NewRequest(req))
		if err != nil {
			return err
		}
		r.a.warnDelivery(name, resp.Msg)
		return nil
	})
}

// deliverPayloads sends a node the payloads its layers issue it as the secrets file has them now.
func (r *rotation) deliverPayloads(ctx context.Context, name string) error {
	t, err := r.target(name)
	if err != nil {
		return err
	}
	payloads, err := payloadsOf(t, r.a.now())
	if err != nil {
		return err
	}
	return r.deliver(ctx, name, &nodev1.ApplyConfigRequest{Layers: payloads})
}

// status reads a node's status.
func (r *rotation) status(ctx context.Context, name string) (*nodev1.StatusResponse, error) {
	nodes, err := r.nodes()
	if err != nil {
		return nil, err
	}
	return nodes.status(ctx, name)
}

var _ layer.Rotating = (*rotation)(nil)

func (r *rotation) Say(format string, args ...any)  { r.say(format, args...) }
func (r *rotation) Secrets() clustersecrets.Secrets { return r.file.secrets }
func (r *rotation) Now() time.Time                  { return r.a.now() }
func (r *rotation) Timeout() time.Duration          { return r.timeout }
func (r *rotation) Poll() time.Duration             { return r.poll }
func (r *rotation) Cluster() layer.Cluster          { return pluginCluster{r.cluster} }
func (r *rotation) NodeNames() []string             { return r.nodeNames() }

func (r *rotation) Call(name string, fn func(conn *client.Conn) error) error {
	return r.call(name, fn)
}

func (r *rotation) Status(ctx context.Context, name string) (*nodev1.StatusResponse, error) {
	return r.status(ctx, name)
}

func (r *rotation) DeliverPayloads(ctx context.Context, name string) error {
	return r.deliverPayloads(ctx, name)
}

func (r *rotation) Nodes() (layer.Nodes, error) {
	nodes, err := r.nodes()
	if err != nil {
		return nil, err
	}
	return pluginNodes{nodes}, nil
}

// publicFile names the public half of the secrets file.
func (r *rotation) publicFile() string {
	if r.file.publicOut != "" {
		return r.file.publicOut
	}
	return "secrets.pub.json"
}
