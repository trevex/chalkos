package storage

import (
	"fmt"
	"sort"
	"strings"
)

// Class says whether a change can be applied to a running node.
type Class int

const (
	// Additive changes keep every byte of existing volumes: repart adds or grows partitions.
	Additive Class = iota
	// Destructive changes need the volume to be wiped and recreated with chalkctl storage reset.
	Destructive
)

func (c Class) String() string {
	if c == Destructive {
		return "destructive"
	}
	return "additive"
}

// Change is the difference of one volume between the recorded and the delivered section.
type Change struct {
	Volume string
	Class  Class
	Reason string
}

func (c Change) String() string {
	return fmt.Sprintf("%s: %s (%s)", c.Volume, c.Reason, c.Class)
}

// Classify compares the section recorded on the node with a newly delivered one. Unchanged
// volumes are left out; a volume with several differences is destructive if any of them is.
func Classify(recorded, delivered Section) []Change {
	var changes []Change
	for _, name := range volumeNames(recorded, delivered) {
		old, hadOld := recorded.Volumes[name]
		cur, hasCur := delivered.Volumes[name]
		switch {
		case !hadOld:
			reason := "new volume"
			if _, known := recorded.Disks[cur.Disk]; !known {
				reason = "new volume on new disk " + cur.Disk
			}
			changes = append(changes, Change{name, Additive, reason})
		case !hasCur:
			changes = append(changes, Change{name, Destructive, "volume removed"})
		default:
			if c, changed := compareVolume(name, old, cur); changed {
				changes = append(changes, c)
			}
		}
	}
	return changes
}

func compareVolume(name string, old, cur Volume) (Change, bool) {
	var additive, destructive []string
	if old.Disk != cur.Disk {
		destructive = append(destructive, fmt.Sprintf("disk %s instead of %s", cur.Disk, old.Disk))
	}
	if old.Format != cur.Format {
		destructive = append(destructive, fmt.Sprintf("format %s instead of %s", orNull(cur.Format), orNull(old.Format)))
	}
	if old.Encryption != cur.Encryption {
		destructive = append(destructive, fmt.Sprintf("encryption %s instead of %s", cur.Encryption, old.Encryption))
	}
	if old.Size != cur.Size && !sameSize(old.Size, cur.Size) {
		reason, grows := compareSize(old.Size, cur.Size)
		if grows {
			additive = append(additive, reason)
		} else {
			destructive = append(destructive, reason)
		}
	}
	if old.MountPoint != cur.MountPoint {
		additive = append(additive, fmt.Sprintf("mounted at %s instead of %s", orNull(cur.MountPoint), orNull(old.MountPoint)))
	}
	switch {
	case len(destructive) > 0:
		return Change{name, Destructive, strings.Join(append(destructive, additive...), "; ")}, true
	case len(additive) > 0:
		return Change{name, Additive, strings.Join(additive, "; ")}, true
	}
	return Change{}, false
}

// compareSize reports whether going from old to cur only grows the partition. Null fills the
// disk, so it is at least as large as any fixed size on the same disk.
func compareSize(old, cur string) (reason string, grows bool) {
	switch {
	case cur == "":
		return "fills its disk instead of " + old, true
	case old == "":
		return "size " + cur + " instead of filling its disk", false
	}
	o, errOld := ParseSize(old)
	c, errCur := ParseSize(cur)
	if errOld != nil || errCur != nil {
		return fmt.Sprintf("size %s instead of %s", cur, old), false
	}
	if c > o {
		return fmt.Sprintf("larger: %s instead of %s", cur, old), true
	}
	return fmt.Sprintf("smaller: %s instead of %s", cur, old), false
}

// sameSize reports whether two sizes are the same number of bytes, such as 1G and 1024M.
func sameSize(a, b string) bool {
	x, errA := ParseSize(a)
	y, errB := ParseSize(b)
	return errA == nil && errB == nil && x == y
}

// ClassifyDisks reports volumes whose changed disk reference resolves to another physical disk
// than the pinned one. Unchanged references are not resolved again.
func ClassifyDisks(delivered Section, pins Pins, resolve func(Ref) (Identity, error)) []Change {
	var changes []Change
	for _, disk := range delivered.DiskNames() {
		pin, pinned := pins.Disks[disk]
		ref := delivered.Disks[disk].Ref
		if disk == SystemDisk || !pinned || pin.Ref == ref {
			continue
		}
		var reason string
		id, err := resolve(ref)
		switch {
		case err != nil:
			reason = fmt.Sprintf("disk reference %s cannot be resolved: %v", ref, err)
		case !id.Same(pin.Identity):
			reason = fmt.Sprintf("disk reference %s resolves to %s, but the volume lives on %s", ref, id, pin.Identity)
		default:
			continue
		}
		for _, name := range sortedVolumes(delivered, disk) {
			changes = append(changes, Change{name, Destructive, reason})
		}
	}
	return changes
}

func volumeNames(a, b Section) []string {
	seen := map[string]bool{}
	var names []string
	for _, s := range []Section{a, b} {
		for name := range s.Volumes {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

func sortedVolumes(s Section, disk string) []string {
	var names []string
	for name, v := range s.Volumes {
		if v.Disk == disk {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func orNull(s string) string {
	if s == "" {
		return "null"
	}
	return s
}
