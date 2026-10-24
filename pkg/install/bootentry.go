package install

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/trevex/chalkos/pkg/storage"
)

var (
	bootEntryRE = regexp.MustCompile(`^Boot([0-9A-Fa-f]{4})\*?\s`)
	bootNextRE  = regexp.MustCompile(`(?m)^BootNext: ([0-9A-Fa-f]{4})`)
	gptPartRE   = regexp.MustCompile(`HD\([0-9a-fA-Fx]+,GPT,([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})`)
)

// bootLabel is the label of the UEFI boot entries the installer adds.
const bootLabel = "chalkos"

type bootEntry struct {
	num, label string
	// partUUID is the GPT partition UUID the entry boots from, lower case; empty when the entry
	// does not name one.
	partUUID string
}

// parseBootEntries reads the entries of efibootmgr --verbose. A label is known only when a tab
// ends it.
func parseBootEntries(out string) []bootEntry {
	var entries []bootEntry
	for _, line := range strings.Split(out, "\n") {
		m := bootEntryRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		e := bootEntry{num: m[1]}
		if label, _, ok := strings.Cut(line[len(m[0]):], "\t"); ok {
			e.label = strings.TrimSpace(label)
		}
		if p := gptPartRE.FindStringSubmatch(line); p != nil {
			e.partUUID = strings.ToLower(p[1])
		}
		entries = append(entries, e)
	}
	return entries
}

// addBootEntry adds a UEFI boot entry for the target's boot loader, unless an earlier attempt
// added it, and makes it the next boot. efibootmgr puts it first in the boot order too, so the
// target keeps booting afterwards.
func (i *Installer) addBootEntry(ctx context.Context, disk storage.BlockDisk) error {
	espPart, err := i.findESP(ctx, disk)
	if err != nil {
		return err
	}
	// The ESP's partition UUID is random, so it names the target's entries alone.
	find := func() (*bootEntry, []bootEntry, error) {
		out, err := i.Run.Run(ctx, "efibootmgr", "--verbose")
		if err != nil {
			return nil, nil, fmt.Errorf("list UEFI boot entries: %w", err)
		}
		entries := parseBootEntries(string(out))
		for n, e := range entries {
			if e.label == bootLabel && e.partUUID == espPart.UUID {
				return &entries[n], entries, nil
			}
		}
		return nil, entries, nil
	}
	created, entries, err := find()
	if err != nil {
		return err
	}
	if created == nil {
		if err := i.change("add the UEFI boot entry"); err != nil {
			return err
		}
		if _, err := i.Run.Run(ctx, "efibootmgr", "--create", "--disk", disk.Device, "--part", strconv.Itoa(espPart.Number), "--label", bootLabel, "--loader", i.loader()); err != nil {
			return fmt.Errorf("add a UEFI boot entry: %w", err)
		}
		if created, entries, err = find(); err != nil {
			return err
		}
		if created == nil {
			return fmt.Errorf("the UEFI boot entry for the ESP %s is missing after creating it", espPart.UUID)
		}
	}
	if err := i.change("boot the target next"); err != nil {
		return err
	}
	if _, err := i.Run.Run(ctx, "efibootmgr", "--bootnext", created.num); err != nil {
		return fmt.Errorf("boot the target next: %w", err)
	}
	i.deleteStaleBootEntries(ctx, entries, created.num)
	return nil
}

// deleteStaleBootEntries deletes the entries earlier installs added whose ESP is on no disk any
// more, such as one whose image was written again. An entry whose ESP is present, or that
// cannot be told to be absent, stays. The node is installed already, so failures are logged.
func (i *Installer) deleteStaleBootEntries(ctx context.Context, entries []bootEntry, keep string) {
	for _, e := range entries {
		if e.num == keep || e.label != bootLabel || e.partUUID == "" {
			continue
		}
		_, err := os.Lstat(filepath.Join(i.Host.DevRoot, "disk", "by-partuuid", e.partUUID))
		if !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if _, err := i.Run.Run(ctx, "efibootmgr", "--delete-bootnum", "--bootnum", e.num); err != nil {
			log.Printf("delete the stale UEFI boot entry Boot%s: %v", e.num, err)
		}
	}
}

// unboot is the first change to a target an earlier attempt left: it removes the boot loader
// that attempt may have written and stops firmware from booting the target next, so a run that
// is refused later never leaves a target that boots a node not installed.
func (i *Installer) unboot(ctx context.Context, target storage.BlockDisk) error {
	espPart, err := i.findESP(ctx, target)
	if err != nil {
		return err
	}
	if err := i.removeBootLoader(ctx, espPart); err != nil {
		return err
	}
	out, err := i.Run.Run(ctx, "efibootmgr", "--verbose")
	if err != nil {
		return fmt.Errorf("list UEFI boot entries: %w", err)
	}
	next := bootNextRE.FindStringSubmatch(string(out))
	if next == nil {
		return nil
	}
	for _, e := range parseBootEntries(string(out)) {
		if e.label == bootLabel && e.partUUID == espPart.UUID && strings.EqualFold(e.num, next[1]) {
			if err := i.change("stop booting the target next"); err != nil {
				return err
			}
			if _, err := i.Run.Run(ctx, "efibootmgr", "--delete-bootnext"); err != nil {
				return fmt.Errorf("stop booting the target next: %w", err)
			}
			return nil
		}
	}
	return nil
}
