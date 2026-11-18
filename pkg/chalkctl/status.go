package chalkctl

import (
	"context"
	"encoding/hex"
	"errors"
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

// ConfigState says whether the running image's slot of the node the status s describes holds the
// config chalkctl delivers to it, as runningAdvice advises.
func (i nodeInventory) ConfigState(s *nodev1.StatusResponse) string {
	name, img := i.name, i.imageOf(s.Boot)
	return i.slotState(s.ConfigVersion, "chalkctl config apply --diff "+name, func(want string) string { return runningAdvice(name, want, img, s) })
}

// StagedConfigState says whether the slot of the image the status s names as staged holds the
// config chalkctl delivers to the node, as stagedAdvice advises.
func (i nodeInventory) StagedConfigState(s *nodev1.StatusResponse) string {
	name, img := i.name, i.imageOf(s.Boot)
	return i.slotState(s.StagedConfigVersion, "chalkctl config apply --staged --diff "+name, func(want string) string { return stagedAdvice(name, want, img, s) })
}

// slotState says whether a slot of the node holds version, the config chalkctl delivers to it, and
// when it does not, what advice says delivers it, given the inventory's version. diff is the command
// that compares the slot's config with the inventory's when status cannot.
func (i nodeInventory) slotState(version, diff string, advice func(want string) string) string {
	c, name := i.c, i.name
	if c.Partial() {
		return "no inventory to compare it with"
	}
	want, err := c.version(name)
	switch {
	case errors.Is(err, errEncryptedSources):
		// status decrypts nothing, so it does not know the config's version.
		return "not compared with the inventory's, which takes Secret values from sops or age sources that status does not decrypt; " + diff + " compares them"
	case err != nil:
		return "cannot read the config the inventory gives it: " + err.Error()
	case want != version:
		return "not the inventory's; " + advice(want)
	}
	return "the inventory's"
}

// trialAdvice says what holds back every delivery to the node of the status s, as trialHold
// tells; "" when nothing does. want is the inventory's config, whose trial's confirmation writes
// the staged slot.
func trialAdvice(name, want string, s *nodev1.StatusResponse) string {
	tr, unreadable := trialHold(s)
	switch {
	case unreadable:
		return "the node takes no delivery until chalkctl config rollback " + name + " removes the record of a config's trial that cannot be read"
	case tr == nil:
		return ""
	case tr.Version == want:
		return "the confirmation of its trial writes it: chalkctl config confirm " + name
	}
	return "the node takes no delivery until the trial of config " + tr.Version + " ends"
}

// runningAdvice says what delivers want, the inventory's config, to the running image's slot.
// config apply checks the config against the image the node runs, which need not accept the
// config of another image, so a node that runs another image than the inventory's gets the config
// with the inventory's image. A known-good image boots with the config it last ran, so the config
// follows its boot.
func runningAdvice(name, want string, img imageMatch, s *nodev1.StatusResponse) string {
	if a := trialAdvice(name, want, s); a != "" {
		return a
	}
	switch {
	case img.runs:
		return "chalkctl config apply " + name + " delivers it" + img.otherChecks()
	case img.stages:
		return "it comes with the image " + img.name + " in the inactive slot"
	case img.knownGood:
		return "the node boots the image " + img.name + " next with the config it last ran; once it runs it, chalkctl config apply " + name + " delivers it"
	}
	return "chalkctl upgrade --nodes " + name + " delivers it with the image " + img.name
}

// stagedAdvice says what delivers want, the inventory's config, to the staged image's slot. A plain
// config apply writes both slots, except that of a rollback's known-good image, which keeps its
// config until the boot is found healthy, as the status names it. A trial holds back every
// delivery but its confirmation. config apply --staged writes the staged slot alone, checked
// against the inventory's image; it is the advice when the running image runs want already, or
// when the staged image is the inventory's and the running one is not and may refuse want. With
// neither image the inventory's, the upgrade delivers want.
func stagedAdvice(name, want string, img imageMatch, s *nodev1.StatusResponse) string {
	b := s.Boot
	if b.GetStagedKnownGood() {
		return "the rollback image keeps the config it last ran while the boot of " + b.GetEntry() + " is not found healthy"
	}
	if a := trialAdvice(name, want, s); a != "" {
		return a
	}
	switch {
	case !img.runs && !img.stages:
		return "chalkctl upgrade --nodes " + name + " delivers it with the image " + img.name
	case s.ConfigVersion == want || !img.runs:
		return "chalkctl config apply --staged " + name + " delivers it" + img.otherChecks()
	}
	return "chalkctl config apply " + name + " delivers it" + img.otherChecks()
}

// imageMatch is what a node's boot status says of the image the inventory names for it.
type imageMatch struct {
	// name and rootHash are the image's name in the inventory and its root hash.
	name, rootHash string
	// runs says whether the node runs the image, and is true when status cannot compare the
	// images.
	runs bool
	// stages says whether the inactive slot holds the image to boot with the config deliveries
	// write there; knownGood whether it holds it as the known-good image, which keeps the config
	// it last ran.
	stages, knownGood bool
	// other names another image in the inactive slot that checks each delivery to the running
	// image the inventory names, as it takes the config too; "" without one.
	other string
	// err says why the inventory's image directory cannot be read.
	err error
}

// otherChecks is what the advice of a config delivery adds for another image in the inactive
// slot, which no command of chalkctl removes and which may refuse the inventory's config.
func (m imageMatch) otherChecks() string {
	if m.other == "" {
		return ""
	}
	return " if the image " + m.other + " in the inactive slot, which is not the inventory's, accepts it too"
}

// imageOf compares the images boot reports with the image the inventory names for the node.
func (i nodeInventory) imageOf(boot *nodev1.BootStatus) imageMatch {
	running := boot.GetRunning().GetRootHash()
	if i.c.Partial() || len(running) == 0 {
		return imageMatch{runs: true}
	}
	nc, md, err := i.c.image(i.name)
	switch {
	case nc == nil:
		return imageMatch{runs: true}
	case err != nil:
		return imageMatch{name: nc.ImageName, runs: true, err: err}
	}
	m := imageMatch{name: nc.ImageName, rootHash: md.RootHash, runs: hex.EncodeToString(running) == md.RootHash}
	staged := boot.GetStaged()
	switch {
	case staged == nil:
	case hex.EncodeToString(staged.GetRootHash()) != md.RootHash:
		if m.runs && !boot.GetStagedKnownGood() {
			m.other = statusview.BootImageName(staged)
		}
	case boot.GetStagedKnownGood():
		m.knownGood = true
	default:
		m.stages = true
	}
	return m
}

// ImageLine is the status line of a node whose store has another root hash than the image the
// inventory names for it, or whose image directory cannot be read; empty when it has that image,
// or when the node or its config is unknown, which the config line reports.
func (i nodeInventory) ImageLine(boot *nodev1.BootStatus) string {
	img := i.imageOf(boot)
	switch {
	case img.err != nil:
		return fmt.Sprintf("its store is not compared with its image in the inventory: %v", img.err)
	case img.runs:
		return ""
	}
	line := fmt.Sprintf("its store's root hash is %x, not that of the image %s the inventory names, %s", boot.GetRunning().GetRootHash(), img.name, img.rootHash)
	staged := statusview.BootImageName(boot.GetStaged())
	// After an upgrade --until stage the image waits for its activation, after --until activate for
	// the node's next boot. A rollback from a boot not found healthy leaves the known-good image to
	// boot next.
	switch {
	case img.stages && boot.GetStagedState() == nodev1.SlotState_SLOT_STATE_STAGED:
		return fmt.Sprintf("%s; it has %s staged: chalkctl node activate %s makes it boot next", line, staged, i.name)
	case img.stages:
		return fmt.Sprintf("%s; it has %s active, which it boots with its next reboot: chalkctl node reboot %s", line, staged, i.name)
	case img.knownGood:
		return fmt.Sprintf("%s; it has %s active as its known-good image, which it boots with the config it last ran at its next reboot: chalkctl node reboot %s", line, staged, i.name)
	}
	return fmt.Sprintf("%s; chalkctl upgrade --nodes %s installs that image", line, i.name)
}
