package chalkctl

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/anypb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/internal/cli"
)

type applyConfigFlags struct {
	node         nodeFlags
	passwordFile string
	staged, diff bool
	rollback     bool
	force        bool
	layers       []layerFlags
	// payloadFlags names the layers' flags that ask for payloads, which --force goes with.
	payloadFlags string
}

// layerFlags are a layer's apply-config flags with the layer that registered them.
type layerFlags struct {
	d layer.Delivery
	f layer.ApplyFlags
}

func (a *app) applyConfigCommand() *cobra.Command {
	f := applyConfigFlags{node: nodeFlags{needs: "apply-config"}}
	cmd := a.command(&cobra.Command{
		Use:     "apply-config <node>",
		Short:   "Deliver a node's config from the inventory",
		Long:    applyConfigLong,
		Example: applyConfigExample,
		Args:    cli.ExactArgs(1),
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
	fs.BoolVar(&f.diff, "diff", false, "print what the config changes in the node's config, document by document and field by field, and change nothing")
	fs.BoolVar(&f.rollback, "rollback", false, "deliver the node's previous config again, the one its image ran before the last delivery, instead of the inventory's")
	return cmd
}

func (a *app) applyConfig(ctx context.Context, f applyConfigFlags, pos []string) error {
	if f.rollback {
		return a.rollbackConfig(ctx, f, pos[0])
	}
	for _, l := range f.layers {
		if err := l.f.Check(f.staged); err != nil {
			return err
		}
	}
	requested := slices.ContainsFunc(f.layers, func(l layerFlags) bool { return l.f.Requested() })
	if f.force && !requested {
		return cli.Usagef("--force goes with %s", f.payloadFlags)
	}
	t, err := a.target(ctx, f.node, pos[0])
	if err != nil {
		return err
	}
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
	if !f.staged && st.Msg.Boot.GetStaged() != nil {
		fmt.Fprintln(a.stderr, stagedNote(t.name, st.Msg))
	}
	if f.staged && st.Msg.Boot.GetStaged() == nil {
		return fmt.Errorf("%s: no image is staged to boot next; apply the config to the running image", t.name)
	}
	nodeDocs, err := slotConfig(ctx, conn, f.staged)
	if err != nil {
		return fmt.Errorf("%s: %w", t.name, err)
	}
	changes := diffConfig(nodeDocs, d.Documents)
	fmt.Fprintf(a.stdout, "%s: %s\n", t.name, changes.summary())
	if f.diff {
		changes.write(a.stdout)
		return nil
	}
	// The rules that compare the config with the node's are checked against the config the node
	// serves before anything is sent, as the node checks them on delivery: against the running
	// slot's, also for the staged image, whose slot the diff compares with.
	baseline := nodeDocs
	if f.staged {
		if baseline, err = slotConfig(ctx, conn, false); err != nil {
			return fmt.Errorf("%s: %w", t.name, err)
		}
	}
	if err := checkRules(c, d.documents, baseline, t.name); err != nil {
		return fmt.Errorf("%s: %w", t.name, err)
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
	resp, err := conn.ApplyConfig(ctx, connect.NewRequest(&nodev1.ApplyConfigRequest{Config: d.Text, FallbackSecret: fallback, Layers: payloads, Staged: f.staged}))
	if err != nil {
		return nodeError(err, t.name)
	}
	for _, c := range resp.Msg.Changes {
		fmt.Fprintf(a.stdout, "volume %s: %s\n", c.Volume, c.Reason)
	}
	for _, u := range resp.Msg.RestartedUnits {
		fmt.Fprintf(a.stdout, "restarted %s\n", u)
	}
	a.warnDelivery(t.name, resp.Msg)
	if f.staged {
		fmt.Fprintf(a.stdout, "%s boots config %s with its staged image\n", t.name, d.Version)
		return nil
	}
	fmt.Fprintf(a.stdout, "%s runs config %s\n", t.name, d.Version)
	return nil
}

// rollbackConfig delivers the node's previous config again, as the node keeps it, and prints what
// it changes in the node's config; with --diff it prints the whole diff and changes nothing.
func (a *app) rollbackConfig(ctx context.Context, f applyConfigFlags, name string) error {
	switch {
	case f.staged:
		return cli.Usagef("--rollback restores the running image's previous config; --staged goes without it")
	case f.force || slices.ContainsFunc(f.layers, func(l layerFlags) bool { return l.f.Requested() }):
		return cli.Usagef("--rollback restores config alone; deliver %s without it", f.payloadFlags)
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
	version := st.Msg.PreviousConfigVersion
	if version == "" {
		return fmt.Errorf("%s keeps no previous config of the image it runs; it keeps one once apply-config replaced the config the image was installed or upgraded with", t.name)
	}
	running, err := slotConfig(ctx, conn, false)
	if err != nil {
		return fmt.Errorf("%s: %w", t.name, err)
	}
	previous, err := previousConfig(ctx, conn)
	if err != nil {
		return fmt.Errorf("%s: %w", t.name, err)
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
	fmt.Fprintf(a.stdout, "%s runs config %s again; config %s is its previous one now\n", t.name, version, st.Msg.ConfigVersion)
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
