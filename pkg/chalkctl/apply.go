package chalkctl

import (
	"cmp"
	"context"
	"fmt"
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
	layers       []layerFlags
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
	for _, d := range compiled[layer.Delivery]() {
		f.layers = append(f.layers, layerFlags{d, d.ApplyFlags(fs)})
	}
	fs.BoolVar(&f.staged, "staged", false, "deliver the config for the image staged to boot next instead, checked against the node's image in the inventory")
	fs.BoolVar(&f.diff, "diff", false, "print what the config changes in the node's config, document by document and field by field, and change nothing")
	return cmd
}

func (a *app) applyConfig(ctx context.Context, f applyConfigFlags, pos []string) error {
	for _, l := range f.layers {
		if err := l.f.Check(f.staged); err != nil {
			return err
		}
	}
	t, err := a.target(ctx, f.node, pos[0])
	if err != nil {
		return err
	}
	for _, l := range f.layers {
		if l.f.Requested() && l.f.Admitted() && !f.diff {
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
		fmt.Fprintf(a.stderr, "%s has a staged image with its own config (version %s); apply-config --staged updates it\n", t.name, cmp.Or(st.Msg.StagedConfigVersion, "none"))
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
