// Package statusview writes a node's status as people read it: what chalkctl status prints and
// the console shows, and the disks of chalkctl disks.
package statusview

import (
	"bytes"
	"cmp"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
)

// Inventory compares what a node reports with what the inventory gives it. chalkctl compares;
// the console has no inventory.
type Inventory interface {
	// ConfigState says whether the running image's slot holds the config chalkctl delivers.
	ConfigState(version string) string
	// StagedConfigState says the same of the slot of the image s names as staged.
	StagedConfigState(s *nodev1.StatusResponse) string
	// PlatformLine is the status line of the platform the node reports.
	PlatformLine(reported string) string
	// ImageLine is the status line of a store other than the inventory's image; empty when there
	// is nothing to say.
	ImageLine(boot *nodev1.BootStatus) string
}

// Options say what Write adds to a node's status.
type Options struct {
	// Node is the node's name, which replaces <node> in the commands a certificate's problem
	// advises.
	Node string
	// Inventory compares the node with the inventory; nil compares nothing.
	Inventory Inventory
	// Layers writes the sections of the image's layers, after the outputs; nil writes none.
	Layers func(w io.Writer) error
}

// Table writes aligned columns, separated by tabs, to w once flushed, as chalkctl's tables do.
func Table(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
}

// Write writes s as chalkctl status prints it.
func Write(w io.Writer, s *nodev1.StatusResponse, o Options) error {
	// A node installed before config documents runs an identity alone.
	config := "config " + cmp.Or(s.ConfigVersion, "none")
	if o.Inventory != nil {
		config += " (" + o.Inventory.ConfigState(s.ConfigVersion) + ")"
	}
	fmt.Fprintln(w, config)
	if s.Trial != nil {
		fmt.Fprintln(w, TrialLine(s.Trial))
	}
	if s.TrialError != "" {
		fmt.Fprintf(w, "the record of a config's trial cannot be read: %s; other changes are refused until config rollback restores the previous config\n", s.TrialError)
	}
	if s.PreviousConfigVersion != "" {
		fmt.Fprintf(w, "previous config %s (config rollback restores it)\n", s.PreviousConfigVersion)
	}
	if s.Boot.GetStaged() != nil {
		staged := "staged config " + cmp.Or(s.StagedConfigVersion, "none")
		if o.Inventory != nil {
			staged += " (" + o.Inventory.StagedConfigState(s) + ")"
		}
		fmt.Fprintln(w, staged)
	}
	if p := s.PendingConfig; p != nil {
		fmt.Fprintln(w, PendingLine(p))
	}
	if o.Inventory != nil {
		fmt.Fprintln(w, o.Inventory.PlatformLine(s.Platform))
	} else {
		fmt.Fprintln(w, "platform "+s.Platform)
	}
	for _, line := range BootLines(s.Boot) {
		fmt.Fprintln(w, line)
	}
	if o.Inventory != nil {
		if line := o.Inventory.ImageLine(s.Boot); line != "" {
			fmt.Fprintln(w, line)
		}
	}
	t := Table(w)
	fmt.Fprintln(t, "VOLUME\tDISK\tMOUNT POINT\tSTATE")
	for _, v := range s.Volumes {
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\n", v.Name, v.Disk, v.MountPoint, VolumeState(v))
	}
	if err := t.Flush(); err != nil {
		return err
	}
	for _, d := range s.Disks {
		if d.Error != "" {
			fmt.Fprintf(w, "disk %s: %s\n", d.Name, d.Error)
		}
	}
	if len(s.Outputs) > 0 {
		t := Table(w)
		fmt.Fprintln(t, "OUTPUT\tHASH\tSTATE")
		for _, out := range s.Outputs {
			fmt.Fprintln(t, OutputLine(out))
		}
		if err := t.Flush(); err != nil {
			return err
		}
	}
	if o.Layers != nil {
		if err := o.Layers(w); err != nil {
			return err
		}
	}
	if len(s.Certificates) > 0 {
		fmt.Fprintln(w, "certificates:")
		t := Table(w)
		for _, c := range s.Certificates {
			fmt.Fprintln(t, CertificateLine(c, o.Node))
		}
		if err := t.Flush(); err != nil {
			return err
		}
	}
	if len(s.Trust) > 0 {
		fmt.Fprintln(w, "trust:")
		t := Table(w)
		for _, tr := range s.Trust {
			fmt.Fprintln(t, TrustLine(tr))
		}
		if err := t.Flush(); err != nil {
			return err
		}
	}
	if s.Time != nil {
		fmt.Fprintln(w, TimeLine(s.Time))
	}
	for _, u := range s.FailedUnits {
		fmt.Fprintf(w, "failed unit %s\n", u)
	}
	return nil
}

// VolumeState is a volume's state in the status's volume table.
func VolumeState(v *nodev1.VolumeStatus) string {
	switch {
	case !v.Present:
		return "missing"
	case v.Mounted:
		return "mounted"
	case v.MountPoint != "":
		return "not mounted"
	}
	return "present"
}

// TrialLine is the status line of a config on trial.
func TrialLine(tr *nodev1.ConfigTrial) string {
	line := fmt.Sprintf("config %s on trial until %s: it rolls back to config %s unless config confirm confirms it", tr.Version, DeadlineOf(tr), tr.PreviousVersion)
	if len(tr.IgnoreUnits) > 0 {
		line += "; its health check leaves out " + strings.Join(tr.IgnoreUnits, ", ")
	}
	switch {
	case tr.Error != "" && tr.RollbackChanged:
		line += "; an attempt to roll the config back failed after it changed the node and is tried again each minute: " + tr.Error +
			"; config confirm no longer keeps the config, and config rollback finishes the rollback now"
	case tr.Error != "":
		line += "; an attempt to roll the config back failed and is tried again each minute: " + tr.Error
	}
	return line
}

// DeadlineOf is the deadline of a config's trial, in UTC.
func DeadlineOf(tr *nodev1.ConfigTrial) string {
	return tr.Deadline.AsTime().UTC().Format(time.RFC3339)
}

// PendingLine is status's line of the config a node holds pending for its next upgrade, which
// nothing checked yet.
func PendingLine(p *nodev1.PendingConfig) string {
	return fmt.Sprintf("pending config %s (unchecked, since %s; the next upgrade checks it against its image)", p.GetVersion(), p.GetSince().AsTime().UTC().Format(time.RFC3339))
}

// BootLines are the status lines of the node's boot: the image it runs, an upgrade it boots next,
// the image a rollback boots, and one it fell back from, with the lines that image logged last.
func BootLines(b *nodev1.BootStatus) []string {
	if b == nil {
		return nil
	}
	running := BootImageName(b.Running)
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
		lines = append(lines, fmt.Sprintf("rollback to %s staged; it boots next", BootImageName(b.Staged)))
	case b.Staged != nil && b.StagedState == nodev1.SlotState_SLOT_STATE_STAGED:
		lines = append(lines, fmt.Sprintf("upgrade to %s staged; node activate makes it boot next", BootImageName(b.Staged)))
	case b.Staged != nil:
		lines = append(lines, fmt.Sprintf("upgrade to %s active; it boots next", BootImageName(b.Staged)))
	}
	if b.Previous != nil {
		lines = append(lines, fmt.Sprintf("previous image %s (upgrade --rollback boots it)", BootImageName(b.Previous)))
	}
	if b.Failed != nil {
		lines = append(lines, fmt.Sprintf("upgrade to %s failed: rolled back to %s", BootImageName(b.Failed), running))
		for _, l := range b.Journal {
			lines = append(lines, "  "+l)
		}
	}
	return lines
}

// CertificateLine is a certificate's line of the status, with tabs between its columns.
func CertificateLine(c *nodev1.CertificateStatus, node string) string {
	line := fmt.Sprintf("  %s\texpires %s", c.Name, c.NotAfter.AsTime().UTC().Format(time.DateOnly))
	if c.NotAfter == nil {
		line = fmt.Sprintf("  %s\texpiry unknown", c.Name)
	}
	if c.Problem != "" {
		line += "\t" + strings.ReplaceAll(c.Problem, "<node>", node)
	}
	return line
}

// TrustLine is a line of the status's trust section: what is trusted, by the first 16 hex digits
// of each fingerprint, the issuing one marked.
func TrustLine(t *nodev1.TrustStatus) string {
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

// TimeLine is the status line of the node's clock.
func TimeLine(t *nodev1.TimeStatus) string {
	switch {
	case t.Error != "":
		return "time: unknown: " + t.Error
	case !t.Synchronised:
		return "time: not synchronised; certificates are checked against this clock"
	}
	return fmt.Sprintf("time: synchronised to %s, offset %+.6f s", t.Source, t.OffsetSeconds)
}

// OutputLine is an output's line of the status: the start of the hash of the files the node
// serves, and whether its last render passed, why it failed, or that none rendered it yet.
func OutputLine(o *nodev1.OutputStatus) string {
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

// shortHash is how many hex digits of a root hash, or of a git revision, name an image.
const shortHash = 12

// ImageName names an image by its store's root hash, with the tag and revision it was built from
// when they are known, such as 3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6). The root hash identifies the
// image; the tag and revision only say where it came from.
func ImageName(rootHash []byte, revision, tag string) string {
	name := hex.EncodeToString(rootHash)
	name = name[:min(len(name), shortHash)]
	if name == "" {
		name = "unknown"
	}
	var about []string
	if tag != "" {
		about = append(about, tag)
	}
	if revision != "" {
		// A flake's dirty revision ends in -dirty, which says more than the digits it cuts.
		r, dirty := strings.CutSuffix(revision, "-dirty")
		r = r[:min(len(r), shortHash)]
		if dirty {
			r += "-dirty"
		}
		about = append(about, r)
	}
	if len(about) > 0 {
		name += " (" + strings.Join(about, ", ") + ")"
	}
	return name
}

// BootImageName names an image a node reports.
func BootImageName(b *nodev1.BootImage) string {
	return ImageName(b.GetRootHash(), b.GetRevision(), b.GetTag())
}

// WriteDisks writes the table of a node's disks, each followed by its partitions.
func WriteDisks(w io.Writer, disks []*nodev1.Disk) error {
	t := Table(w)
	fmt.Fprintln(t, "DEVICE\tSIZE\tTYPE\tMODEL\tSERIAL\tWWN\tUSE")
	for _, d := range disks {
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.Device, Size(d.Size), d.Type, d.Model, d.Serial, d.Wwn, d.Usage)
		for _, part := range d.Partitions {
			fmt.Fprintf(t, "  %s\t%s\t%s\t%s\t\t\t\n", part.Device, Size(part.Size), part.Label, part.Content)
		}
	}
	return t.Flush()
}

// Size is a number of bytes in binary units, with one decimal where it has one, such as 1.5G.
func Size(n uint64) string {
	units := []string{"B", "K", "M", "G", "T", "P"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0") + units[i]
}
