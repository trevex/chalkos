package chalkctl

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
)

type applyConfigFlags struct {
	node         nodeFlags
	passwordFile string
	staged, diff bool
	// dryRun has the node check the config and print the delivery's plan in place of applying it.
	dryRun   bool
	rollback bool
	force    bool
	layers   []layerFlags
	// payloadFlags names the layers' flags that ask for payloads, which --force goes with.
	payloadFlags string
	// try is how long the config is on trial; zero applies it for good. noConfirm leaves the
	// trial open; confirm confirms an open one. ignoreUnits are the units the trial's health
	// check leaves out.
	try                time.Duration
	noConfirm, confirm bool
	ignoreUnits        []string
}

// payloads reports whether a layer's flag asks for payloads.
func (f applyConfigFlags) payloads() bool {
	return slices.ContainsFunc(f.layers, func(l layerFlags) bool { return l.f.Requested() })
}

// checkTrial refuses what --try, --no-confirm and --ignore-unit do not go with. The node checks
// the units' names.
func (f applyConfigFlags) checkTrial() error {
	switch {
	case f.noConfirm && f.try == 0:
		return cli.Usagef("--no-confirm goes with --try")
	case len(f.ignoreUnits) > 0 && f.try == 0:
		return cli.Usagef("--ignore-unit goes with --try")
	case f.try == 0:
		return nil
	case f.try < minTrial || f.try > maxTrial:
		return cli.Usagef("--try takes from %v to %v, not %v", minTrial, maxTrial, f.try)
	case f.staged:
		return cli.Usagef("--try applies config to the running image; --staged goes without it")
	case f.diff:
		return cli.Usagef("--diff changes nothing, so it tries nothing; drop --try")
	case f.force || f.payloads():
		return cli.Usagef("--try delivers config alone, which a rollback undoes; deliver %s without it", f.payloadFlags)
	}
	return nil
}

// layerFlags are a layer's apply-config flags with the layer that registered them.
type layerFlags struct {
	d layer.Delivery
	f layer.ApplyFlags
}

func (a *app) applyConfigCommand() *cobra.Command {
	f := applyConfigFlags{node: nodeFlags{needs: "apply-config"}}
	cmd := a.command(&cobra.Command{
		Use:     "apply-config <node> [<node>...]",
		Short:   "Deliver a node's config from the inventory",
		Long:    applyConfigLong,
		Example: applyConfigExample,
		Args:    cli.RangeArgs(1, math.MaxInt),
	}, func(a *app, ctx context.Context, pos []string) error { return a.applyConfig(ctx, f, pos) })
	fs := cmd.Flags()
	f.node.register(fs)
	registerPasswordFile(fs, &f.passwordFile)
	var payloadFlags []string
	for _, d := range compiled[layer.Delivery]() {
		lf := d.ApplyFlags(fs)
		f.layers = append(f.layers, layerFlags{d, lf})
		payloadFlags = append(payloadFlags, lf.Flag())
	}
	if len(payloadFlags) > 0 {
		f.payloadFlags = strings.Join(payloadFlags, " or ")
		fs.BoolVar(&f.force, "force", false, "with "+f.payloadFlags+", deliver without asking whether the node is revoked")
	}
	fs.BoolVar(&f.staged, "staged", false, "deliver the config for the image staged to boot next instead, checked against the node's image in the inventory")
	fs.BoolVar(&f.diff, "diff", false, "print what the config changes in the node's config, document by document and field by field, and change nothing; the node checks nothing, which --dry-run has it do")
	fs.BoolVar(&f.dryRun, "dry-run", false, "have the node check the config as it checks a delivery, its renderers and facts included, print the diff and what the delivery would change, and change nothing; takes several nodes")
	fs.BoolVar(&f.rollback, "rollback", false, "deliver the node's previous config again, the one its image ran before the last delivery, instead of the inventory's")
	fs.DurationVar(&f.try, "try", 0, "apply the config on trial for the duration, from 10s to 24h: the node rolls it back by itself unless chalkctl confirms it over a new connection once the node is healthy")
	fs.BoolVar(&f.noConfirm, "no-confirm", false, "with --try, leave the trial open for apply-config --confirm")
	fs.BoolVar(&f.confirm, "confirm", false, "confirm the node's open trial once the node is healthy, so it keeps the config on trial")
	fs.StringArrayVar(&f.ignoreUnits, "ignore-unit", nil, "with --try, leave the unit out of the health check the confirmation waits for, as the image's check leaves out the units it ignores")
	return cmd
}

func (a *app) applyConfig(ctx context.Context, f applyConfigFlags, pos []string) error {
	if len(pos) > 1 && !f.dryRun {
		return cli.Usagef("apply-config delivers to one node; --dry-run checks several, one after the other")
	}
	switch {
	case f.dryRun:
		if err := f.checkDryRun(); err != nil {
			return err
		}
	case f.confirm:
		return a.confirmConfig(ctx, f, pos[0])
	case f.rollback:
		return a.rollbackConfig(ctx, f, pos[0])
	}
	if err := f.checkTrial(); err != nil {
		return err
	}
	for _, l := range f.layers {
		if err := l.f.Check(f.staged); err != nil {
			return err
		}
	}
	if f.force && !f.payloads() {
		return cli.Usagef("--force goes with %s", f.payloadFlags)
	}
	if f.dryRun {
		return a.dryRunConfig(ctx, f, pos)
	}
	t, err := a.target(ctx, f.node, pos[0])
	if err != nil {
		return err
	}
	return a.deliverConfig(ctx, f, t)
}

// checkDryRun refuses what --dry-run does not go with: it checks the inventory's config for the
// running image alone, as a delivery without a trial would deliver it.
func (f applyConfigFlags) checkDryRun() error {
	switch {
	case f.staged:
		return cli.Usagef("--dry-run checks config for the running image; --staged goes without it")
	case f.diff:
		return cli.Usagef("--dry-run prints the diff too; drop --diff")
	case f.rollback || f.confirm:
		return cli.Usagef("--dry-run checks the inventory's config; --rollback and --confirm go without it")
	case f.try != 0 || f.noConfirm || len(f.ignoreUnits) > 0:
		return cli.Usagef("--dry-run tries nothing; drop --try, --no-confirm and --ignore-unit")
	case f.force || f.payloads():
		return cli.Usagef("--dry-run checks config alone; deliver %s without it", f.payloadFlags)
	}
	return nil
}

// dryRunConfig has each node of names check the inventory's config as its delivery, one after
// the other, and prints what each delivery would change. A node that refuses its config does not
// stop the others.
func (a *app) dryRunConfig(ctx context.Context, f applyConfigFlags, names []string) error {
	if len(names) > 1 && f.node.address != "" {
		return cli.Usagef("--address reaches one node; check the nodes one by one")
	}
	c, err := f.node.loadCluster(ctx)
	if err != nil {
		return err
	}
	creds, err := a.loadSecretCredentials(ctx, f.node.secrets)
	if err != nil {
		return err
	}
	var refused, failed []string
	for _, name := range names {
		t, err := targetIn(c, f.node.address, name, creds)
		if err == nil {
			err = a.deliverConfig(ctx, f, t)
		}
		switch {
		case err != nil && len(names) == 1:
			return err
		case err != nil:
			fmt.Fprintf(a.stderr, "chalkctl: %s\n", namedOnce(name, err))
			if errors.As(err, new(refusal)) {
				refused = append(refused, name)
			} else {
				failed = append(failed, name)
			}
		}
	}
	var msgs []string
	if len(refused) > 0 {
		msgs = append(msgs, fmt.Sprintf("the config of %s is refused: %s", layer.Count(len(refused), "node"), strings.Join(refused, ", ")))
	}
	if len(failed) > 0 {
		msgs = append(msgs, fmt.Sprintf("the dry run failed on %s: %s", layer.Count(len(failed), "node"), strings.Join(failed, ", ")))
	}
	if len(msgs) > 0 {
		return errors.New(strings.Join(msgs, "; "))
	}
	return nil
}

// refusal is a dry run's refusal of a node's config: by the image's rules chalkctl checks, or by
// the node's checks. A dry run that fails before the config is checked, as of a node missing from
// the inventory, unreachable or on trial, refuses nothing.
type refusal struct{ error }

func (r refusal) Unwrap() error { return r.error }

// refusedByNode reports whether the node's answer to a dry run refuses the config, as it would
// refuse the delivery, rather than failing for the node's own reasons, such as an upgrade that
// runs or a fault of its own.
func refusedByNode(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeInvalidArgument, connect.CodeFailedPrecondition:
		return connect.IsWireError(err)
	}
	return false
}

// namedErr marks an error whose message names the node it is about already, as the errors of the
// node's trial, of the config it holds and of chalkctl's check of the image's rules do. It changes
// no message; namedError, by contrast, puts the name in place of <node>.
type namedErr struct{ error }

func (e namedErr) Unwrap() error { return e.error }

// namedOnce is err as a line of several nodes' output names the node called name: prefixed with
// the name unless err, or an error it wraps, is a namedErr.
func namedOnce(name string, err error) string {
	if errors.As(err, new(namedErr)) {
		return err.Error()
	}
	return name + ": " + err.Error()
}

// deliverConfig delivers the inventory's config to the node t, or with --dry-run has the node
// check it and prints what the delivery would change.
func (a *app) deliverConfig(ctx context.Context, f applyConfigFlags, t *target) error {
	for _, l := range f.layers {
		if l.f.Requested() && !f.force && !f.diff {
			if err := l.d.Admit(ctx, pluginTarget{t}, l.f.Does()); err != nil {
				return err
			}
		}
	}
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	defer conn.Close()
	// The staged image is the node's image in the inventory, as after an upgrade without a reboot;
	// the node checks the config against the staged image itself again, and refuses it without one.
	var c contract
	if f.staged {
		c, err = t.cluster.imageContractOf(t.name)
	} else {
		c, err = nodeContract(ctx, conn, t.name)
	}
	if err != nil {
		return err
	}
	d, err := t.cluster.deliveryFor(t.name, c)
	if err != nil {
		return err
	}
	st, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		return err
	}
	// The node refuses every delivery while a config is on trial; --diff changes nothing.
	if !f.diff {
		if err := trialOpen(t.name, st.Msg); err != nil {
			return err
		}
	}
	if f.try > 0 && st.Msg.ConfigVersion == d.Version {
		return namedErr{fmt.Errorf("%s runs config %s already; --try needs a config that changes it", t.name, d.Version)}
	}
	// A unit that failed before the trial keeps its confirmation from passing, also later with
	// --confirm, unless it recovers or is left out.
	if f.try > 0 {
		if w := failedUnitsWarning(st.Msg.FailedUnits, f.ignoreUnits); w != "" {
			fmt.Fprintf(a.stderr, "chalkctl: warning: %s: %s\n", t.name, w)
		}
	}
	if !f.staged && st.Msg.Boot.GetStaged() != nil {
		fmt.Fprintln(a.stderr, stagedNote(t.name, st.Msg))
	}
	if f.staged && st.Msg.Boot.GetStaged() == nil {
		return namedErr{fmt.Errorf("%s: no image is staged to boot next; apply the config to the running image", t.name)}
	}
	nodeDocs, err := slotConfig(ctx, conn, f.staged)
	if err != nil {
		return namedErr{fmt.Errorf("%s: %w", t.name, err)}
	}
	changes := diffConfig(nodeDocs, d.Documents)
	fmt.Fprintf(a.stdout, "%s: %s\n", t.name, changes.summary())
	if f.diff || f.dryRun {
		changes.write(a.stdout)
	}
	if f.diff {
		return nil
	}
	// The rules that compare the config with the node's are checked against the config the node
	// serves before anything is sent, as the node checks them on delivery: against the running
	// slot's, also for the staged image, whose slot the diff compares with.
	baseline := nodeDocs
	if f.staged {
		if baseline, err = slotConfig(ctx, conn, false); err != nil {
			return namedErr{fmt.Errorf("%s: %w", t.name, err)}
		}
	}
	if err := checkRules(c, d.documents, baseline, t.name); err != nil {
		return refusal{namedErr{fmt.Errorf("%s: %w", t.name, err)}}
	}
	fallback, err := a.fallbackOf(ctx, st.Msg, t, d.identity.Storage, f.passwordFile, "")
	if err != nil {
		return err
	}
	var payloads []*anypb.Any
	for _, l := range f.layers {
		if !l.f.Requested() {
			continue
		}
		p, err := l.d.Payloads(pluginTarget{t}, time.Now())
		if err != nil {
			return err
		}
		if len(p) == 0 {
			return l.f.None(t.name)
		}
		payloads = append(payloads, p...)
	}
	req := &nodev1.ApplyConfigRequest{Config: d.Text, FallbackSecret: fallback, Layers: payloads, Staged: f.staged, DryRun: f.dryRun}
	// chalkctl's deadline starts before the node's, so it falls before it. An answer that comes
	// after it is of no use: the node may have rolled the config back by then.
	deadline := a.now().Add(f.try)
	applyCtx := ctx
	if f.try > 0 {
		req.Try, req.IgnoreUnits = durationpb.New(f.try), f.ignoreUnits
		var cancel context.CancelFunc
		applyCtx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
		fmt.Fprintf(a.stdout, "%s: delivering config %s on trial until %s\n", t.name, d.Version, deadline.UTC().Format(time.RFC3339))
	}
	resp, err := conn.ApplyConfig(applyCtx, connect.NewRequest(req))
	if err != nil {
		if f.try > 0 {
			return a.failedTrial(ctx, conn, t, d.Version, st.Msg.ConfigVersion, deadline, err)
		}
		if f.dryRun && refusedByNode(err) {
			return refusal{nodeError(err, t.name)}
		}
		return nodeError(err, t.name)
	}
	if f.dryRun {
		writePlan(a.stdout, t.name, resp.Msg)
		a.warnDelivery(t.name, resp.Msg)
		return nil
	}
	for _, c := range resp.Msg.Changes {
		fmt.Fprintf(a.stdout, "volume %s: %s\n", c.Volume, c.Reason)
	}
	for _, u := range resp.Msg.RestartedUnits {
		fmt.Fprintf(a.stdout, "restarted %s\n", u)
	}
	a.warnDelivery(t.name, resp.Msg)
	switch {
	case f.staged:
		fmt.Fprintf(a.stdout, "%s boots config %s with its staged image\n", t.name, d.Version)
		return nil
	case f.try == 0:
		fmt.Fprintf(a.stdout, "%s runs config %s\n", t.name, d.Version)
		return nil
	}
	fmt.Fprintf(a.stdout, "%s runs config %s on trial; it rolls back to config %s by itself unless confirmed within %v\n", t.name, d.Version, st.Msg.ConfigVersion, f.try)
	if f.noConfirm {
		fmt.Fprintln(a.stdout, endTrialHint(t.name))
		return nil
	}
	// The delivery's connection survives what the config changes, as established connections do
	// through a firewall; only a new one proves the node reachable.
	conn.Close()
	return a.confirmTrial(ctx, t, d.Version, st.Msg.ConfigVersion, deadline, false)
}

// failedTrial returns err, which failed the delivery of config version on trial, which would
// replace config previous until deadline. A delivery the node refused after it changed the config
// leaves the config on trial until its deadline, as err says; chalkctl then does not confirm a
// config that failed to apply, and names the commands that end the trial. The node's status is
// read on conn, or on a new connection when conn fails. An answer that did not come, as when the
// config cut the connection off or the node answered after the deadline, may be of a config the
// node applied: chalkctl does not confirm it either, and waits for the node to end the trial, as
// after a confirmation that did not go through.
func (a *app) failedTrial(ctx context.Context, conn *client.Conn, t *target, version, previous string, deadline time.Time, err error) error {
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("%w; %s may run config %s on trial until %s; %s", err, t.name, version, deadline.UTC().Format(time.RFC3339), endTrialHint(t.name))
	case !connect.IsWireError(err):
		conn.Close()
		return a.trialRolledBack(ctx, t, version, previous, deadline, fmt.Errorf("%s: config %s was not confirmed within its trial: the answer to its delivery did not come: %w", t.name, version, err))
	}
	err = nodeError(err, t.name)
	st, serr := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
	if serr != nil {
		if c, derr := dialInstalled(t); derr == nil {
			st, serr = c.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
			c.Close()
		}
	}
	if serr == nil && st.Msg.Trial.GetVersion() == version {
		return fmt.Errorf("%w; %s", err, endTrialHint(t.name))
	}
	return err
}

// writePlan prints the plan of a dry run of the node called name: the config it would replace and
// run, then the outputs, the effects with their actions and what fired them, and the storage
// changes, each section "none" when it has nothing.
func writePlan(w io.Writer, name string, resp *nodev1.ApplyConfigResponse) {
	p := resp.GetPlan()
	fmt.Fprintf(w, "%s: config %s → %s\n", name, cmp.Or(p.GetReplaces(), "none"), cmp.Or(p.GetVersion(), "none"))
	section := func(label string, lines []string) {
		if len(lines) == 0 {
			lines = []string{"none"}
		}
		for i, l := range lines {
			if i > 0 {
				label = ""
			}
			fmt.Fprintf(w, "  %-10s%s\n", label, l)
		}
	}
	var outputs []string
	if len(p.GetOutputs()) > 0 {
		outputs = []string{strings.Join(p.GetOutputs(), ", ")}
	}
	section("outputs:", outputs)
	var effects []string
	for _, e := range p.GetEffects() {
		effects = append(effects, fmt.Sprintf("%s: %s (%s)", e.Name, strings.Join(e.Actions, ", "), strings.Join(e.FiredBy, ", ")))
	}
	section("effects:", effects)
	var storage []string
	for _, c := range resp.GetChanges() {
		storage = append(storage, fmt.Sprintf("volume %s: %s", c.Volume, c.Reason))
	}
	section("storage:", storage)
}

// endTrialHint names the commands that end the node's open trial.
func endTrialHint(name string) string {
	return fmt.Sprintf("chalkctl apply-config --confirm %s keeps it; chalkctl apply-config --rollback %s rolls it back now", name, name)
}

// rollbackConfig delivers the node's previous config again, as the node keeps it, and prints what
// it changes in the node's config; with --diff it prints the whole diff and changes nothing.
func (a *app) rollbackConfig(ctx context.Context, f applyConfigFlags, name string) error {
	switch {
	case f.staged:
		return cli.Usagef("--rollback restores the running image's previous config; --staged goes without it")
	case f.force || f.payloads():
		return cli.Usagef("--rollback restores config alone; deliver %s without it", f.payloadFlags)
	case f.try != 0 || f.noConfirm || len(f.ignoreUnits) > 0:
		return cli.Usagef("--rollback goes without --try, --no-confirm and --ignore-unit")
	case f.passwordFile != "":
		return cli.Usagef("--rollback adds no volume, so it takes no --password-file")
	}
	// The node keeps the config it rolls back to, so no inventory needs to give it.
	n := f.node
	n.needs = ""
	t, err := a.target(ctx, n, name)
	if err != nil {
		return err
	}
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	defer conn.Close()
	st, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		return nodeError(err, t.name)
	}
	version, replaced := st.Msg.PreviousConfigVersion, st.Msg.ConfigVersion
	// During a trial the node rolls back to the config from before it alone. A node that runs that
	// config already, as a rollback at the deadline that failed after it wrote the config leaves
	// it, delivers it again to finish that rollback, and the config on trial stays its previous.
	tr := st.Msg.Trial
	if tr != nil {
		version = tr.PreviousVersion
		if replaced == version {
			replaced = tr.Version
		}
	}
	// A trial whose record the node cannot read names no config from before it, so the rollback
	// goes to the previous config, which a rollback at the trial's deadline may have made the config
	// on trial; a second rollback then undoes it. A node that keeps no previous config delivers the
	// config it runs again, which removes the record as well.
	if e := st.Msg.TrialError; e != "" {
		if version != "" {
			fmt.Fprintf(a.stderr, "chalkctl: warning: %s: the record of a config's trial cannot be read: %s; the rollback delivers config %s, the node's previous config, which is the config on trial if a rollback at the trial's deadline replaced it already; if it is, a second apply-config --rollback undoes it\n", t.name, e, version)
		} else if version = st.Msg.ConfigVersion; version != "" {
			fmt.Fprintf(a.stderr, "chalkctl: warning: %s: the record of a config's trial cannot be read: %s; the node keeps no previous config, so the rollback delivers config %s, the config it runs, again\n", t.name, e, version)
		}
	}
	if version == "" {
		return fmt.Errorf("%s keeps no previous config of the image it runs; it keeps one once apply-config replaced the config the image was installed or upgraded with", t.name)
	}
	running, err := slotConfig(ctx, conn, false)
	if err != nil {
		return fmt.Errorf("%s: %w", t.name, err)
	}
	previous := running
	if version != st.Msg.ConfigVersion {
		if previous, err = previousConfig(ctx, conn); err != nil {
			return fmt.Errorf("%s: %w", t.name, err)
		}
	}
	changes := diffConfig(running, previous)
	fmt.Fprintf(a.stdout, "%s: %s\n", t.name, changes.summary())
	if f.diff {
		changes.writeRemoved(a.stdout, "in the running config, not in the previous one")
		return nil
	}
	resp, err := conn.RollbackConfig(ctx, connect.NewRequest(&nodev1.RollbackConfigRequest{Version: version}))
	if err != nil {
		return nodeError(err, t.name)
	}
	for _, c := range resp.Msg.Changes {
		fmt.Fprintf(a.stdout, "volume %s: %s\n", c.Volume, c.Reason)
	}
	for _, u := range resp.Msg.RestartedUnits {
		fmt.Fprintf(a.stdout, "restarted %s\n", u)
	}
	a.warnDelivery(t.name, &nodev1.ApplyConfigResponse{Warnings: resp.Msg.Warnings})
	if version == replaced {
		fmt.Fprintf(a.stdout, "%s runs config %s, delivered again\n", t.name, version)
	} else {
		fmt.Fprintf(a.stdout, "%s runs config %s again; config %s is its previous one now\n", t.name, version, replaced)
	}
	if tr != nil {
		fmt.Fprintf(a.stdout, "%s: the trial of config %s ended\n", t.name, tr.Version)
	}
	return nil
}

// warnDelivery prints why the rule of one of the node's effects could not be evaluated, whose
// effect the node took all the same.
func (a *app) warnDelivery(name string, resp *nodev1.ApplyConfigResponse) {
	for _, w := range resp.GetWarnings() {
		fmt.Fprintf(a.stderr, "chalkctl: warning: %s: %s\n", name, w)
	}
}

type resetVolumeFlags struct {
	node         nodeFlags
	passwordFile string
}

func (a *app) resetVolumeCommand() *cobra.Command {
	f := resetVolumeFlags{node: nodeFlags{needs: "storage reset"}}
	cmd := a.command(&cobra.Command{
		Use:     "reset <node> <volume>",
		Short:   "Wipe and recreate one volume",
		Long:    resetVolumeLong,
		Example: resetVolumeExample,
		Args:    cli.ExactArgs(2),
	}, func(a *app, ctx context.Context, pos []string) error { return a.resetVolume(ctx, f, pos) })
	f.node.registerClient(cmd.Flags())
	registerPasswordFile(cmd.Flags(), &f.passwordFile)
	return cmd
}

func (a *app) resetVolume(ctx context.Context, f resetVolumeFlags, pos []string) error {
	t, err := a.clientTarget(ctx, f.node, pos[0])
	if err != nil {
		return err
	}
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	defer conn.Close()
	c, err := nodeContract(ctx, conn, t.name)
	if err != nil {
		return err
	}
	d, err := t.cluster.deliveryFor(t.name, c)
	if err != nil {
		return err
	}
	fallback, err := a.fallbackFor(ctx, conn, t, d.identity.Storage, f.passwordFile, pos[1])
	if err != nil {
		return err
	}
	if _, err := conn.ResetVolume(ctx, connect.NewRequest(&nodev1.ResetVolumeRequest{Volume: pos[1], Config: d.Text, FallbackSecret: fallback})); err != nil {
		return nodeError(err, t.name)
	}
	fmt.Fprintf(a.stdout, "volume %s of %s was wiped and created again\n", pos[1], t.name)
	return nil
}

// stagedNote is the line apply-config prints of a node whose status names an image staged to boot
// next, which keeps its own config. The image a rollback staged while the running image's boot is
// not found healthy is the one the node falls back to, which keeps the config it last ran, so
// apply-config --staged is not offered for it.
func stagedNote(name string, st *nodev1.StatusResponse) string {
	b := st.GetBoot()
	if !b.GetBlessed() && bytes.Equal(b.GetRollback().GetRootHash(), b.GetStaged().GetRootHash()) {
		return fmt.Sprintf("%s boots the image a rollback staged next, with the config it last ran (version %s), which stays until the boot of the running image is found healthy", name, cmp.Or(st.GetStagedConfigVersion(), "none"))
	}
	return fmt.Sprintf("%s has a staged image with its own config (version %s); apply-config --staged updates it", name, cmp.Or(st.GetStagedConfigVersion(), "none"))
}
