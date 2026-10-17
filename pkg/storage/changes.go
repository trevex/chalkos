package storage

import (
	"fmt"
	"sort"
	"strings"
)

// Change is what a delivered section changes of one volume, as the node makes it: a new volume,
// a larger one, or a volume mounted elsewhere. Which changes a node accepts is the image's rules'
// to say; Changes describes those it makes.
type Change struct {
	Volume string
	Reason string
}

func (c Change) String() string {
	return fmt.Sprintf("%s: %s", c.Volume, c.Reason)
}

// Changes lists what the delivered section adds to the recorded one, by volume name: new volumes,
// grown sizes and moved mount points. Other differences, such as another format, are no
// change the node makes; the rules refuse them before.
func Changes(recorded, delivered Section) []Change {
	var changes []Change
	names := make([]string, 0, len(delivered.Volumes))
	for name := range delivered.Volumes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
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

func orNull(s string) string {
	if s == "" {
		return "null"
	}
	return s
}
