package chalkctl

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
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
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/internal/cli"
)

type applyConfigFlags struct {
	node         nodeFlags
	passwordFile string
	staged, diff bool
	// dryRun has the node check the config and print the delivery's plan in place of applying it.
	dryRun bool
	force  bool
	layers []layerFlags
	// payloadFlags names the layers' flags that ask for payloads, which --force goes with.
	payloadFlags string
	// try is how long the config is on trial; zero applies it for good. noConfirm leaves the
	// trial open. ignoreUnits are the units the trial's health check leaves out.
	try         time.Duration
	noConfirm   bool
	ignoreUnits []string
	// until is how far a delivery takes the nodes: untilDeliver leaves the first node it delivers
	// to on trial.
	until applyStep
	// rollout are the values of the Rollout layers' flags, by layer.
	rollout map[string]any
}

// payloads reports whether a layer's flag asks for payloads.
func (f applyConfigFlags) payloads() bool {
	return slices.ContainsFunc(f.layers, func(l layerFlags) bool { return l.f.Requested() })
}

// layerFlags are a layer's flags of config apply with the layer that registered them.
type layerFlags struct {
	d layer.Delivery
	f layer.ApplyFlags
}

// applyConfigCommand returns config apply, or with short its short form, chalkctl apply. Each call
// builds a command of its own with its own flags, so both can be in the tree.
func (a *app) applyConfigCommand(short bool) *cobra.Command {
	f := applyConfigFlags{node: nodeFlags{needs: "config apply"}, until: untilConfirm, rollout: map[string]any{}}
	cmd := &cobra.Command{
		Use:     "apply <node> [<node>...]",
		Short:   "Deliver a node's config from the inventory",
		Long:    applyConfigLong,
		Example: applyConfigExample,
		Args:    cli.MinimumNArgs(1),
	}
	if short {
		f.node.needs = "apply"
		cmd.Short = "Deliver a node's config from the inventory: short for config apply"
		cmd.Long = applyShortLong + "\n\n" + applyConfigLong
		cmd.Example = strings.ReplaceAll(applyConfigExample, "chalkctl config apply ", "chalkctl apply ")
	}
	a.command(cmd, func(a *app, ctx context.Context, pos []string) error { return a.applyConfig(ctx, f, pos) })
	fs := cmd.Flags()
	f.node.Register(fs)
	setAddressUsage(fs, "address of the node's chalkd, host or host:port, or NODE=ADDR of any node of the cluster; repeat it for several (default each node's address in the inventory)")
	registerPasswordFile(fs, &f.passwordFile)
	var payloadFlags, payloadNames []string
	for _, d := range compiled[layer.Delivery]() {
		lf := d.ApplyFlags(fs)
		f.layers = append(f.layers, layerFlags{d, lf})
		payloadFlags = append(payloadFlags, lf.Flag())
		payloadNames = append(payloadNames, strings.TrimPrefix(lf.Flag(), "--"))
	}
	if len(payloadFlags) > 0 {
		f.payloadFlags = strings.Join(payloadFlags, " or ")
		fs.BoolVar(&f.force, "force", false, "with "+f.payloadFlags+", deliver without asking whether the node is revoked")
	}
	fs.BoolVar(&f.staged, "staged", false, "deliver the config for the image staged in the inactive slot instead, active or not, which checks it with its own chalkd after chalkctl checks it against the node's image in the inventory; without a staged image, the node holds it pending, unchecked, for the next upgrade, which checks it against its image; with --dry-run, the staged image checks it and nothing is kept; without one, --dry-run says what the node would hold pending")
	fs.BoolVar(&f.diff, "diff", false, "print what the config changes in the node's config, document by document and field by field, and change nothing; the node checks nothing, which --dry-run has it do")
	fs.BoolVar(&f.dryRun, "dry-run", false, "have the node check the config as it checks a delivery, its renderers and facts included, print the diff and what the delivery would change, and change nothing; with --staged, the staged image checks it; takes several nodes")
	fs.DurationVar(&f.try, "try", 0, "apply the config on trial for the duration, from 10s to 24h: the node rolls it back by itself unless chalkctl confirms it over a new connection once the node is healthy")
	fs.BoolVar(&f.noConfirm, "no-confirm", false, "with --try, leave the trial open for config confirm")
	fs.StringArrayVar(&f.ignoreUnits, "ignore-unit", nil, "with --try, leave the unit out of the health check the confirmation waits for, as the image's check leaves out the units it ignores")
	fs.Var(cli.Enum(&f.until, "step", untilDeliver, untilConfirm), "until", "how far the delivery goes: deliver delivers to the first node that does not run the config, on trial, and leaves the trial open for config confirm; confirm delivers to every node")
	for _, p := range plugins {
		if l, ok := p.(layer.Rollout); ok {
			f.rollout[p.Name()] = l.RolloutFlags(fs)
		}
	}
	// A trial applies config alone to the running image and changes it; a dry run checks config
	// alone and prints the diff. --no-confirm and --ignore-unit go with --try, and --force with a
	// payload flag, but not the other way round, which applyConfig checks.
	cmd.MarkFlagsMutuallyExclusive("dry-run", "diff")
	for _, other := range append([]string{"staged", "diff", "dry-run"}, payloadNames...) {
		cmd.MarkFlagsMutuallyExclusive("try", other)
	}
	for _, name := range payloadNames {
		cmd.MarkFlagsMutuallyExclusive("dry-run", name)
	}
	return cmd
}

func (a *app) applyConfig(ctx context.Context, f applyConfigFlags, pos []string) error {
	// A node named twice gets the config once, as upgrade --nodes upgrades it once.
	var names []string
	for _, name := range pos {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	pos = names
	several := len(pos) > 1
	// The node checks the names of the units --ignore-unit leaves out.
	switch {
	case several && (f.staged || f.diff):
		return cli.Usagef("--staged and --diff take one node")
	case several && f.noConfirm:
		return cli.Usagef("--no-confirm leaves one node's trial open; with several nodes, --until deliver does")
	case f.until == untilDeliver && (f.dryRun || f.staged || f.diff || f.noConfirm):
		return cli.Usagef("--until deliver delivers the config on trial; it goes without --dry-run, --staged, --diff and --no-confirm")
	case f.noConfirm && f.try == 0:
		return cli.Usagef("--no-confirm goes with --try")
	case len(f.ignoreUnits) > 0 && f.try == 0:
		return cli.Usagef("--ignore-unit goes with --try")
	case f.try != 0 && (f.try < minTrial || f.try > maxTrial):
		return cli.Usagef("--try takes from %v to %v, not %v", minTrial, maxTrial, f.try)
	case f.force && !f.payloads():
		return cli.Usagef("--force goes with %s", f.payloadFlags)
	}
	for _, l := range f.layers {
		if err := l.f.Check(f.staged); err != nil {
			return err
		}
	}
	switch {
	case f.dryRun:
		return a.dryRunConfig(ctx, f, pos)
	case f.staged || f.diff:
		t, err := a.target(ctx, f.node, pos[0])
		if err != nil {
			return err
		}
		return a.deliverConfig(ctx, f, t)
	}
	return a.deliverToNodes(ctx, f, pos)
}

// dryRunConfig has each node of names check the inventory's config as its delivery, one after
// the other, and prints what each delivery would change. A node that refuses its config does not
// stop the others.
func (a *app) dryRunConfig(ctx context.Context, f applyConfigFlags, names []string) error {
	s, err := a.reach(ctx, f.node, soleNode(names), false)
	if err != nil {
		return err
	}
	var refused, failed []string
	for _, name := range names {
		t, err := s.target(name)
		if err == nil {
			err = a.deliverConfig(ctx, f, t)
		}
		switch {
		case err != nil && len(names) == 1:
			return err
		case err != nil:
			fmt.Fprintf(a.stderr, "chalkctl: %s\n", layer.Named(name, err))
			if refusesConfig(err) {
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

// refusesConfig reports whether err refuses a node's config, as a layer.NodeError says: a dry run counts
// it apart from a failure before the config is checked, as of a node missing from the inventory,
// unreachable or on trial, which refuses nothing; other commands print it as any error.
func refusesConfig(err error) bool {
	for e, ok := errors.AsType[*layer.NodeError](err); ok; e, ok = errors.AsType[*layer.NodeError](e.Err) {
		if e.Refused {
			return true
		}
	}
	return false
}

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

// deliverConfig delivers the inventory's config to the node t, or with --dry-run has the node
// check it and prints what the delivery would change.
func (a *app) deliverConfig(ctx context.Context, f applyConfigFlags, t *target) error {
	for _, l := range f.layers {
		if l.f.Requested() && !f.force && !f.diff {
			if err := l.d.Admit(ctx, t, l.f.Does()); err != nil {
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
	// the node checks the config against the staged image itself again, and without one holds it
	// pending, unchecked, for the next upgrade's image to check.
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
	a.warnUnpinned(t.name, d)
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
		return &layer.NodeError{Node: t.name, Err: fmt.Errorf("%s runs config %s already; --try needs a config that changes it", t.name, d.Version), Named: true}
	}
	// A unit that failed before the trial keeps its confirmation from passing, also later with
	// config confirm, unless it recovers or is left out.
	if f.try > 0 {
		if w := failedUnitsWarning(st.Msg.FailedUnits, f.ignoreUnits); w != "" {
			fmt.Fprintf(a.stderr, "chalkctl: warning: %s: %s\n", t.name, w)
		}
	}
	// Without a staged image, --staged holds the config pending for the next upgrade, which takes
	// it in place of the running config: the diff compares with the pending config, or else with
	// the running one.
	pending := f.staged && st.Msg.Boot.GetStaged() == nil
	nodeDocs, err := slotConfig(ctx, conn, f.staged)
	if err == nil && pending && nodeDocs.Len() == 0 {
		nodeDocs, err = slotConfig(ctx, conn, false)
	}
	if err != nil {
		return layer.Named(t.name, err)
	}
	changes := diffConfig(nodeDocs, config.Redact(d.Documents))
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
			return layer.Named(t.name, err)
		}
	}
	if err := checkRules(c, d.documents, baseline, t.name); err != nil {
		return &layer.NodeError{Node: t.name, Err: err, Refused: true}
	}
	// A pending config is kept without the fallback secret: the upgrade that adds an encrypted
	// volume carries it. A staged image that goes between the Status read and the delivery, as an
	// upgrade that fails retires it, has the node hold the config pending, which its answer names;
	// a fallback secret sent for that image is refused by holdPending, which says no image is
	// staged, and the delivery run again holds the config without one.
	var fallback string
	if !pending {
		if fallback, err = a.fallbackOf(ctx, st.Msg, t, d.identity.Storage, f.passwordFile, ""); err != nil {
			return err
		}
	}
	var payloads []*anypb.Any
	for _, l := range f.layers {
		if !l.f.Requested() {
			continue
		}
		p, err := l.d.Payloads(t, time.Now())
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
	deadline := time.Now().Add(f.try)
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
			return &layer.NodeError{Node: t.name, Err: err, Refused: true}
		}
		return layer.Named(t.name, err)
	}
	// The node answers by what it found: an image staged in between takes the config as any
	// --staged delivery, and a pending config is named whatever chalkctl expected.
	if p := resp.Msg.Pending; p != nil && f.staged {
		if f.dryRun {
			fmt.Fprintf(a.stdout, "%s: no image is staged; config %s would be held pending, unchecked, for the next upgrade, which checks it against its image\n", t.name, p.Version)
		} else {
			fmt.Fprintf(a.stdout, "%s holds config %s pending, unchecked; the next upgrade checks it against its image\n", t.name, p.Version)
		}
		return nil
	}
	if f.dryRun {
		writePlan(a.stdout, t.name, resp.Msg, f.staged)
		a.warnDelivery(t.name, resp.Msg)
		return nil
	}
	printApplied(a.stdout, resp.Msg.Changes, resp.Msg.RestartedUnits)
	a.warnDelivery(t.name, resp.Msg)
	switch {
	case f.staged:
		fmt.Fprintf(a.stdout, "%s boots config %s with its staged image\n", t.name, d.Version)
		return nil
	case f.try == 0:
		fmt.Fprintf(a.stdout, "%s runs config %s\n", t.name, d.Version)
		writeSecond(a.stdout, t.name, resp.Msg, "")
		return nil
	}
	fmt.Fprintf(a.stdout, "%s runs config %s on trial; it rolls back to config %s by itself unless confirmed within %v\n", t.name, d.Version, st.Msg.ConfigVersion, f.try)
	writeSecond(a.stdout, t.name, resp.Msg, " once the trial is confirmed")
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
	err = layer.Named(t.name, err)
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

// writeSecond prints what a delivery of config to the running image of the node called name, which
// resp answers, wrote besides, or with when, such as once a trial is confirmed, will write: the
// staged image's slot, which that image checked, or the pending config.
func writeSecond(w io.Writer, name string, resp *nodev1.ApplyConfigResponse, when string) {
	if s := resp.GetStaged(); s != nil {
		fmt.Fprintf(w, "%s boots config %s with its staged image too%s\n", name, s.GetVersion(), when)
	}
	if p := resp.GetPending(); p != nil {
		fmt.Fprintf(w, "%s holds config %s pending%s, unchecked, in place of its pending config; the next upgrade checks it against its image\n", name, p.GetVersion(), when)
	}
}

// writePlan prints the plan of a dry run of the node called name: the config it would replace and
// run, then the outputs, the effects with their actions and what fired them, and the storage
// changes, each section "none" when it has nothing. The plan of a dry run for the staged image
// has its config and the storage changes alone: the image renders its outputs and takes no
// effects until it boots. The plan of one for the running image ends with the staged image's
// config, which that image checked, or the pending config the delivery would replace.
func writePlan(w io.Writer, name string, resp *nodev1.ApplyConfigResponse, staged bool) {
	p := resp.GetPlan()
	which := "config"
	if staged {
		which = "staged image's config"
	}
	fmt.Fprintf(w, "%s: %s %s → %s\n", name, which, cmp.Or(p.GetReplaces(), "none"), cmp.Or(p.GetVersion(), "none"))
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
	if !staged {
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
	}
	var storage []string
	for _, c := range resp.GetChanges() {
		storage = append(storage, fmt.Sprintf("volume %s: %s", c.Volume, c.Reason))
	}
	section("storage:", storage)
	if s := resp.GetStaged(); s != nil {
		fmt.Fprintf(w, "%s: staged image's config %s → %s\n", name, cmp.Or(s.GetReplaces(), "none"), s.GetVersion())
	}
	if p := resp.GetPending(); p != nil {
		fmt.Fprintf(w, "%s: pending config → %s, unchecked; the next upgrade checks it against its image\n", name, p.GetVersion())
	}
}

// endTrialHint names the commands that end the node's open trial.
func endTrialHint(name string) string {
	return fmt.Sprintf("chalkctl config confirm %s keeps it; chalkctl config rollback %s rolls it back now", name, name)
}

type rollbackConfigFlags struct {
	// node needs no inventory: the node keeps the config it rolls back to.
	node nodeFlags
	diff bool
}

func (a *app) rollbackConfigCommand() *cobra.Command {
	var f rollbackConfigFlags
	cmd := a.command(&cobra.Command{
		Use:     "rollback <node>",
		Short:   "Deliver a node's previous config again",
		Long:    rollbackConfigLong,
		Example: rollbackConfigExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.rollbackConfig(ctx, f, pos[0]) })
	f.node.Register(cmd.Flags())
	cmd.Flags().BoolVar(&f.diff, "diff", false, "print what the rollback changes in the node's config, document by document and field by field, and change nothing")
	return cmd
}

// rollbackConfig delivers the node's previous config again, as the node keeps it, and prints what
// it changes in the node's config; with --diff it prints the whole diff and changes nothing.
func (a *app) rollbackConfig(ctx context.Context, f rollbackConfigFlags, name string) error {
	t, err := a.target(ctx, f.node, name)
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
		return layer.Named(t.name, err)
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
			fmt.Fprintf(a.stderr, "chalkctl: warning: %s: the record of a config's trial cannot be read: %s; the rollback delivers config %s, the node's previous config, which is the config on trial if a rollback at the trial's deadline replaced it already; if it is, a second config rollback undoes it\n", t.name, e, version)
		} else if version = st.Msg.ConfigVersion; version != "" {
			fmt.Fprintf(a.stderr, "chalkctl: warning: %s: the record of a config's trial cannot be read: %s; the node keeps no previous config, so the rollback delivers config %s, the config it runs, again\n", t.name, e, version)
		}
	}
	if version == "" {
		return fmt.Errorf("%s keeps no previous config of the image it runs; it keeps one once config apply replaced the config the image was installed or upgraded with", t.name)
	}
	running, err := slotConfig(ctx, conn, false)
	if err != nil {
		return layer.Named(t.name, err)
	}
	previous := running
	if version != st.Msg.ConfigVersion {
		if previous, err = getConfig(ctx, conn, &nodev1.GetConfigRequest{Previous: true}); err != nil {
			return layer.Named(t.name, err)
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
		return layer.Named(t.name, err)
	}
	printApplied(a.stdout, resp.Msg.Changes, resp.Msg.RestartedUnits)
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

// printApplied prints the storage changes and the restarted units of a delivery.
func printApplied(w io.Writer, changes []*nodev1.StorageChange, units []string) {
	for _, c := range changes {
		fmt.Fprintf(w, "volume %s: %s\n", c.Volume, c.Reason)
	}
	for _, u := range units {
		fmt.Fprintf(w, "restarted %s\n", u)
	}
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
	f.node.RegisterClient(cmd.Flags())
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
		return layer.Named(t.name, err)
	}
	fmt.Fprintf(a.stdout, "volume %s of %s was wiped and created again\n", pos[1], t.name)
	return nil
}

func (a *app) discardPendingCommand() *cobra.Command {
	// The node holds the config it drops, so no inventory needs to give it.
	var n nodeFlags
	cmd := a.command(&cobra.Command{
		Use:     "discard <node>",
		Short:   "Drop the config a node holds pending for its next upgrade",
		Long:    discardPendingLong,
		Example: discardPendingExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.discardPending(ctx, n, pos[0]) })
	n.Register(cmd.Flags())
	return cmd
}

// discardPending has the node drop the config it holds pending and says which it dropped.
func (a *app) discardPending(ctx context.Context, n nodeFlags, name string) error {
	t, err := a.target(ctx, n, name)
	if err != nil {
		return err
	}
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := conn.ApplyConfig(ctx, connect.NewRequest(&nodev1.ApplyConfigRequest{Staged: true, DiscardPending: true}))
	if err != nil {
		return layer.Named(t.name, err)
	}
	if p := resp.Msg.Pending; p != nil {
		fmt.Fprintf(a.stdout, "%s dropped its pending config %s\n", t.name, p.Version)
		return nil
	}
	fmt.Fprintf(a.stdout, "%s holds no pending config\n", t.name)
	return nil
}
