package chalkctl

import (
	"bytes"
	"cmp"
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/internal/cli"
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
	n.registerClient(cmd.Flags())
	return cmd
}

func (a *app) status(ctx context.Context, n nodeFlags, pos []string) error {
	t, err := a.clientTarget(ctx, n, pos[0])
	if err != nil {
		return err
	}
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := conn.Status(ctx, connect.NewRequest(&nodev1.StatusRequest{}))
	if err != nil {
		return layer.Named(t.name, err)
	}
	s := resp.Msg
	state := t.cluster.configState(t.name, s.ConfigVersion)
	// A node installed before config documents runs an identity alone.
	fmt.Fprintf(a.stdout, "config %s (%s)\n", cmp.Or(s.ConfigVersion, "none"), state)
	if s.Trial != nil {
		fmt.Fprintln(a.stdout, trialLine(s.Trial))
	}
	if s.TrialError != "" {
		fmt.Fprintf(a.stdout, "the record of a config's trial cannot be read: %s; other changes are refused until apply-config --rollback restores the previous config\n", s.TrialError)
	}
	if s.PreviousConfigVersion != "" {
		fmt.Fprintf(a.stdout, "previous config %s (apply-config --rollback restores it)\n", s.PreviousConfigVersion)
	}
	if s.Boot.GetStaged() != nil {
		fmt.Fprintf(a.stdout, "staged config %s (%s)\n", cmp.Or(s.StagedConfigVersion, "none"), t.cluster.stagedConfigState(t.name, s))
	}
	if p := s.PendingConfig; p != nil {
		fmt.Fprintln(a.stdout, pendingLine(p))
	}
	fmt.Fprintln(a.stdout, t.cluster.platformLine(t.name, s.Platform))
	for _, line := range bootLines(s.Boot) {
		fmt.Fprintln(a.stdout, line)
	}
	if line := t.cluster.imageLine(t.name, s.Boot); line != "" {
		fmt.Fprintln(a.stdout, line)
	}
	w := a.table()
	fmt.Fprintln(w, "VOLUME\tDISK\tMOUNT POINT\tSTATE")
	for _, v := range s.Volumes {
		st := "present"
		switch {
		case !v.Present:
			st = "missing"
		case v.Mounted:
			st = "mounted"
		case v.MountPoint != "":
			st = "not mounted"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", v.Name, v.Disk, v.MountPoint, st)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	for _, d := range s.Disks {
		if d.Error != "" {
			fmt.Fprintf(a.stdout, "disk %s: %s\n", d.Name, d.Error)
		}
	}
	if len(s.Outputs) > 0 {
		w := a.table()
		fmt.Fprintln(w, "OUTPUT\tHASH\tSTATE")
		for _, o := range s.Outputs {
			fmt.Fprintln(w, outputLine(o))
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	info, err := conn.Info(ctx, connect.NewRequest(&nodev1.InfoRequest{}))
	if err != nil {
		return err
	}
	for _, p := range pluginsOf[layer.Status](info.Msg.Layers) {
		if err := p.PrintStatus(ctx, host{a}, conn); err != nil {
			return err
		}
	}
	if len(s.Certificates) > 0 {
		fmt.Fprintln(a.stdout, "certificates:")
		w := a.table()
		for _, c := range s.Certificates {
			fmt.Fprintln(w, certificateLine(c, t.name))
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	if len(s.Trust) > 0 {
		fmt.Fprintln(a.stdout, "trust:")
		w := a.table()
		for _, tr := range s.Trust {
			fmt.Fprintln(w, trustLine(tr))
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	if s.Time != nil {
		fmt.Fprintln(a.stdout, timeLine(s.Time))
	}
	for _, u := range s.FailedUnits {
		fmt.Fprintf(a.stdout, "failed unit %s\n", u)
	}
	return nil
}

// bootLines are the status lines of the node's boot: the image it runs, an upgrade it boots next,
// the image a rollback boots, and one it fell back from, with the lines that image logged last.
func bootLines(b *nodev1.BootStatus) []string {
	if b == nil {
		return nil
	}
	running := bootImageName(b.Running)
	if b.Error != "" {
		return []string{fmt.Sprintf("image %s; its boot is unknown: %s", running, b.Error)}
	}
	line := fmt.Sprintf("image %s, booted from %s", running, b.Entry)
	if !b.Blessed {
		line += ", not found healthy yet"
	}
	lines := []string{line}
	switch {
	case b.Staged != nil && bytes.Equal(b.GetRollback().GetRootHash(), b.Staged.GetRootHash()):
		lines = append(lines, fmt.Sprintf("rollback to %s staged; it boots next", bootImageName(b.Staged)))
	case b.Staged != nil && b.StagedState == nodev1.SlotState_SLOT_STATE_STAGED:
		lines = append(lines, fmt.Sprintf("upgrade to %s staged; node activate makes it boot next", bootImageName(b.Staged)))
	case b.Staged != nil:
		lines = append(lines, fmt.Sprintf("upgrade to %s active; it boots next", bootImageName(b.Staged)))
	}
	if b.Previous != nil {
		lines = append(lines, fmt.Sprintf("previous image %s (upgrade --rollback boots it)", bootImageName(b.Previous)))
	}
	if b.Failed != nil {
		lines = append(lines, fmt.Sprintf("upgrade to %s failed: rolled back to %s", bootImageName(b.Failed), running))
		for _, l := range b.Journal {
			lines = append(lines, "  "+l)
		}
	}
	return lines
}

// certificateLine is a certificate's line of the status, with tabs between its columns.
func certificateLine(c *nodev1.CertificateStatus, node string) string {
	line := fmt.Sprintf("  %s\texpires %s", c.Name, c.NotAfter.AsTime().UTC().Format(time.DateOnly))
	if c.NotAfter == nil {
		line = fmt.Sprintf("  %s\texpiry unknown", c.Name)
	}
	if c.Problem != "" {
		line += "\t" + strings.ReplaceAll(c.Problem, "<node>", node)
	}
	return line
}

// trustLine is a line of the status's trust section: what is trusted, by the first 16 hex digits
// of each fingerprint, the issuing one marked.
func trustLine(t *nodev1.TrustStatus) string {
	var fps []string
	for _, fp := range t.Fingerprints {
		short := fp[:min(16, len(fp))]
		if fp == t.Issuing {
			short += " (issues)"
		}
		fps = append(fps, short)
	}
	return fmt.Sprintf("  %s\t%s", t.Name, strings.Join(fps, ", "))
}

// timeLine is the status line of the node's clock.
func timeLine(t *nodev1.TimeStatus) string {
	switch {
	case t.Error != "":
		return "time: unknown: " + t.Error
	case !t.Synchronised:
		return "time: not synchronised; certificates are checked against this clock"
	}
	return fmt.Sprintf("time: synchronised to %s, offset %+.6f s", t.Source, t.OffsetSeconds)
}

// outputLine is an output's line of the status: the start of the hash of the files the node
// serves, and whether its last render passed, why it failed, or that none rendered it yet.
func outputLine(o *nodev1.OutputStatus) string {
	hash := "none"
	if len(o.Hash) >= 12 {
		hash = o.Hash[:12]
	}
	state := "rendered"
	switch {
	case o.Error != "":
		state = "failed: " + o.Error
	case o.Hash == "":
		state = "not rendered yet"
	}
	return o.Name + "\t" + hash + "\t" + state
}

// platformLine is the status line of the platform the node runs on, compared with its config's.
func (c *cluster) platformLine(name, reported string) string {
	if c.partial() {
		return "platform " + reported
	}
	want, err := c.platform(name)
	switch {
	case err != nil:
		return fmt.Sprintf("platform %s; the platform of its config's image is unknown: %v", reported, err)
	case reported != want:
		return fmt.Sprintf("platform %s, but %s; changing a node's platform is a reinstall", reported, c.declares(name, want))
	}
	return fmt.Sprintf("platform %s (the inventory's)", reported)
}

// pendingLine is status's line of the config a node holds pending for its next upgrade, which
// nothing checked yet.
func pendingLine(p *nodev1.PendingConfig) string {
	return fmt.Sprintf("pending config %s (unchecked, since %s; the next upgrade checks it against its image)", p.GetVersion(), p.GetSince().AsTime().UTC().Format(time.RFC3339))
}

// configState says whether the running image's slot of a node holds the config chalkctl delivers
// to it.
func (c *cluster) configState(name, version string) string {
	return c.slotState(name, version, func(string) string { return "chalkctl apply-config " + name + " delivers it" })
}

// stagedConfigState says whether the slot of the image the status s names as staged holds the
// config chalkctl delivers to the node, as stagedAdvice advises.
func (c *cluster) stagedConfigState(name string, s *nodev1.StatusResponse) string {
	return c.slotState(name, s.StagedConfigVersion, func(want string) string { return stagedAdvice(name, want, s) })
}

// slotState says whether a slot of a node holds version, the config chalkctl delivers to it, and
// when it does not, what advice says delivers it, given the inventory's version.
func (c *cluster) slotState(name, version string, advice func(want string) string) string {
	if c.partial() {
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

// stagedAdvice is what delivers want, the inventory's config, to the slot of the image the status
// s names as staged, which holds other config. A plain apply-config writes both images' slots, but
// not that of the known-good image a rollback from a boot not yet found healthy staged, which
// keeps the config it last ran until that boot is found healthy; chalkd names that image in the
// status, by the test its deliveries take. While a trial is open the node takes no delivery, and
// the trial's confirmation writes the staged slot it held back: the advice is the confirmation
// when want is on trial, and the trial's end otherwise. When the running image runs want already,
// only the staged slot differs, as after a write of that slot that failed, and apply-config
// --staged writes it alone.
func stagedAdvice(name, want string, s *nodev1.StatusResponse) string {
	b := s.Boot
	switch {
	case b.GetStagedKnownGood():
		return "the rollback image keeps the config it last ran while the boot of " + b.GetEntry() + " is not found healthy"
	case s.Trial != nil && s.Trial.Version == want:
		return "the confirmation of its trial writes it: chalkctl apply-config --confirm " + name
	case s.Trial != nil:
		return "the node takes no delivery until the trial of config " + s.Trial.Version + " ends"
	case s.ConfigVersion == want:
		return "chalkctl apply-config --staged " + name + " delivers it"
	}
	return "chalkctl apply-config " + name + " delivers it"
}

// imageLine is the status line of a node whose store has another root hash than the image the
// inventory names for it, or whose image directory cannot be read; empty when it has that image,
// or when the node or its config is unknown, which the config line reports.
func (c *cluster) imageLine(name string, boot *nodev1.BootStatus) string {
	running := boot.GetRunning().GetRootHash()
	if c.partial() || len(running) == 0 {
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
			return fmt.Sprintf("%s; it has %s staged: chalkctl node activate %s makes it boot next", line, bootImageName(boot.GetStaged()), name)
		}
		return fmt.Sprintf("%s; it has %s active, which it boots with its next reboot: chalkctl node reboot %s", line, bootImageName(boot.GetStaged()), name)
	}
	return fmt.Sprintf("%s; chalkctl upgrade --nodes %s installs that image", line, name)
}
