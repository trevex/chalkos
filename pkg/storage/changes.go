package storage

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Change is one volume that the delivered section adds, grows, mounts elsewhere or mounts with
// exec or noexec instead. The image's rules decide which changes are allowed.
type Change struct {
	Volume string
	Reason string
}

func (c Change) String() string {
	return fmt.Sprintf("%s: %s", c.Volume, c.Reason)
}

// Changes lists the delivered section's changes to the recorded one, by volume name. The rules
// refuse any other difference first.
func Changes(recorded, delivered Section) []Change {
	var changes []Change
	for _, name := range slices.Sorted(maps.Keys(delivered.Volumes)) {
		cur := delivered.Volumes[name]
		old, hadOld := recorded.Volumes[name]
		if !hadOld {
			reason := "new volume"
			if _, known := recorded.Disks[cur.Disk]; !known {
				reason = "new volume on new disk " + cur.Disk
			}
			changes = append(changes, Change{name, reason})
			continue
		}
		var reasons []string
		if reason, grows := growth(old.Size, cur.Size); grows {
			reasons = append(reasons, reason)
		}
		if old.MountPoint != cur.MountPoint {
			reasons = append(reasons, fmt.Sprintf("mounted at %s instead of %s", orNull(cur.MountPoint), orNull(old.MountPoint)))
		}
		if old.Exec != cur.Exec {
			reasons = append(reasons, fmt.Sprintf("mounted %s instead of %s", execOption(cur.Exec), execOption(old.Exec)))
		}
		if len(reasons) > 0 {
			changes = append(changes, Change{name, strings.Join(reasons, "; ")})
		}
	}
	return changes
}

// growth reports whether going from old to cur grows the partition. Null fills the disk, so it is
// at least as large as any fixed size on the same disk; sizes of the same bytes, as 1G and 1024M,
// are the same.
func growth(old, cur string) (reason string, grows bool) {
	switch {
	case old == cur:
		return "", false
	case cur == "":
		return "fills its disk instead of " + old, true
	case old == "":
		return "", false
	}
	o, errOld := ParseSize(old)
	c, errCur := ParseSize(cur)
	if errOld != nil || errCur != nil || c <= o {
		return "", false
	}
	return fmt.Sprintf("larger: %s instead of %s", cur, old), true
}

func execOption(exec bool) string {
	if exec {
		return "exec"
	}
	return "noexec"
}

func orNull(s string) string {
	if s == "" {
		return "null"
	}
	return s
}
