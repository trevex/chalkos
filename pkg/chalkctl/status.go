package chalkctl

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/statusview"
)

func (a *app) statusCommand() *cobra.Command {
	var n nodeFlags
	cmd := a.command(&cobra.Command{
		Use:     "status <node>",
		Short:   "Show an installed node's status",
		Long:    statusLong,
		Example: statusExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.status(ctx, n, pos) })
	n.RegisterClient(cmd.Flags())
	return cmd
}

func (a *app) status(ctx context.Context, n nodeFlags, pos []string) error {
	t, err := a.clientTarget(ctx, n, pos[0])
	if err != nil {
		return err
	}
	return callNode(t, func(conn *client.Conn) error { return a.printStatus(ctx, t, conn) })
}

// printStatus prints the status of the node t over conn.
func (a *app) printStatus(ctx context.Context, t *target, conn *client.Conn) error {
	resp, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		return err
	}
	return statusview.Write(a.stdout, resp.Msg, statusview.Options{
		Node:      t.name,
		Inventory: nodeInventory{c: t.cluster, name: t.name},
		Layers: func(io.Writer) error {
			info, err := conn.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
			if err != nil {
				return err
			}
			for _, p := range pluginsOf[layer.Status](info.Msg.Layers) {
				if err := p.PrintStatus(ctx, host{a}, conn); err != nil {
					return err
				}
			}
			return nil
		},
	})
}

// nodeInventory compares the node name with the inventory of c, as statusview asks.
type nodeInventory struct {
	c    *cluster
	name string
}

// PlatformLine is the status line of the platform the node runs on, compared with its config's.
func (i nodeInventory) PlatformLine(reported string) string {
	c, name := i.c, i.name
	if c.Partial() {
		return "platform " + reported
	}
	nc, md, err := c.image(name)
	switch {
	case err != nil:
		return fmt.Sprintf("platform %s; the platform of its config's image is unknown: %v", reported, err)
	case reported != md.Platform:
		return fmt.Sprintf("platform %s, but its image %s is built for %s; changing a node's platform is a reinstall", reported, nc.ImageName, md.Platform)
	}
	return fmt.Sprintf("platform %s (the inventory's)", reported)
}

// ConfigState says whether the running image's slot of a node holds the config chalkctl delivers
// to it.
func (i nodeInventory) ConfigState(version string) string {
	name := i.name
	return i.slotState(version, func(string) string { return "chalkctl config apply " + name + " delivers it" })
}

// StagedConfigState says whether the slot of the image the status s names as staged holds the
// config chalkctl delivers to the node, as stagedAdvice advises.
func (i nodeInventory) StagedConfigState(s *nodev1.StatusResponse) string {
	name := i.name
	return i.slotState(s.StagedConfigVersion, func(want string) string { return stagedAdvice(name, want, s) })
}

// slotState says whether a slot of the node holds version, the config chalkctl delivers to it, and
// when it does not, what advice says delivers it, given the inventory's version.
func (i nodeInventory) slotState(version string, advice func(want string) string) string {
	c, name := i.c, i.name
	if c.Partial() {
		return "no inventory to compare it with"
	}
	want, err := c.version(name)
	switch {
	case err != nil:
		return "cannot read the config the inventory gives it: " + err.Error()
	case want != version:
		return "not the inventory's; " + advice(want)
	}
	return "the inventory's"
}

// stagedAdvice says what delivers want, the inventory's config, to the staged image's slot. A plain
// config apply writes both slots, except that of a rollback's known-good image, which keeps its
// config until the boot is found healthy, as the status names it. During a trial the advice is its
// confirmation when want is on trial and its end otherwise. When the running image runs want
// already, config apply --staged writes the staged slot alone.
func stagedAdvice(name, want string, s *nodev1.StatusResponse) string {
	b := s.Boot
	switch {
	case b.GetStagedKnownGood():
		return "the rollback image keeps the config it last ran while the boot of " + b.GetEntry() + " is not found healthy"
	case s.Trial != nil && s.Trial.Version == want:
		return "the confirmation of its trial writes it: chalkctl config confirm " + name
	case s.Trial != nil:
		return "the node takes no delivery until the trial of config " + s.Trial.Version + " ends"
	case s.ConfigVersion == want:
		return "chalkctl config apply --staged " + name + " delivers it"
	}
	return "chalkctl config apply " + name + " delivers it"
}

// ImageLine is the status line of a node whose store has another root hash than the image the
// inventory names for it, or whose image directory cannot be read; empty when it has that image,
// or when the node or its config is unknown, which the config line reports.
func (i nodeInventory) ImageLine(boot *nodev1.BootStatus) string {
	c, name := i.c, i.name
	running := boot.GetRunning().GetRootHash()
	if c.Partial() || len(running) == 0 {
		return ""
	}
	nc, err := c.config(name)
	if err != nil {
		return ""
	}
	md, err := nc.Resolve()
	switch {
	case err != nil:
		return fmt.Sprintf("its store is not compared with its image in the inventory: %v", err)
	case hex.EncodeToString(running) == md.RootHash:
		return ""
	}
	line := fmt.Sprintf("its store's root hash is %x, not that of the image %s the inventory names, %s", running, nc.ImageName, md.RootHash)
	// After an upgrade --until stage the image waits for its activation, after --until activate for
	// the node's next boot.
	if want, err := hex.DecodeString(md.RootHash); err == nil && bytes.Equal(boot.GetStaged().GetRootHash(), want) {
		if boot.GetStagedState() == nodev1.SlotState_SLOT_STATE_STAGED {
			return fmt.Sprintf("%s; it has %s staged: chalkctl node activate %s makes it boot next", line, statusview.BootImageName(boot.GetStaged()), name)
		}
		return fmt.Sprintf("%s; it has %s active, which it boots with its next reboot: chalkctl node reboot %s", line, statusview.BootImageName(boot.GetStaged()), name)
	}
	return fmt.Sprintf("%s; chalkctl upgrade --nodes %s installs that image", line, name)
}
