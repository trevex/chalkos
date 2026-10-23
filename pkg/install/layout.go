package install

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// GPT partition types of the names repart's Type= takes that the system region uses.
var partitionTypes = map[string]string{
	"esp":               typeESP,
	"usr-x86-64":        typeUsrX86,
	"usr-x86-64-verity": typeUsrX86Verity,
	"usr-arm64":         typeUsrArm,
	"usr-arm64-verity":  typeUsrArmVerity,
}

// storeTypes are the GPT partition types of a store slot's data and hash tree partitions, by the
// architecture an image is built for.
var storeTypes = map[string]struct{ data, verity string }{
	"x86-64": {typeUsrX86, typeUsrX86Verity},
	"arm64":  {typeUsrArm, typeUsrArmVerity},
}

// definitionKeys are the keys of repart's [Partition] section a definition of the system region
// may set, by the kind of partition it defines: what chalkos's own definitions set, and
// AddValidateFS on STATE, which only stops repart from marking its file system for
// systemd-validatefs. repart runs as root on the installer, so a key that copies files or blocks
// from the host, makes directories or subvolumes, or lays out anything but these partitions is
// refused, as is any key not listed. Encrypt on STATE is replaced with the node's policy.
var definitionKeys = map[string]map[string]bool{
	"esp":   {"Type": true, "Label": true, "Format": true, "SizeMinBytes": true, "SizeMaxBytes": true},
	"store": {"Type": true, "Label": true, "SizeMinBytes": true, "SizeMaxBytes": true},
	"state": {"Type": true, "Label": true, "Format": true, "Encrypt": true, "AddValidateFS": true, "SizeMinBytes": true, "SizeMaxBytes": true},
}

// definedPartition is a partition of the system region as a repart definition describes it.
type definedPartition struct {
	file, typ, label, format string
	// size is the partition's size in bytes when the definition fixes it, and 0 otherwise.
	size int64
	// maxSize is the most bytes the definition gives the partition, and 0 when it sets no limit.
	maxSize int64
}

// layout is the system region the image's definitions lay out, in their order: the ESP, store
// slots A and B, and STATE.
type layout []definedPartition

// parseLayout reads the image's definitions of the system region and checks that they lay out
// exactly what an install writes: an ESP formatted as vfat, the data and hash tree partitions of
// two store slots for arch, and STATE formatted as ext4, each with no keys but those
// definitionKeys allows.
func parseLayout(defs map[string]string, arch string) (layout, error) {
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)
	var l layout
	for _, name := range names {
		d, err := parseDefinition(name, defs[name])
		if err != nil {
			return nil, err
		}
		l = append(l, d)
	}
	store, ok := storeTypes[arch]
	if !ok {
		return nil, fmt.Errorf("chalkos builds no images for the architecture %q", arch)
	}
	count := map[string]int{}
	for _, d := range l {
		switch d.typ {
		case typeESP, store.data, store.verity, storage.PartitionType(stateLabel):
			count[d.typ]++
		default:
			return nil, fmt.Errorf("%s defines a partition of the type %s, which is none of the system region of an image for %s", d.file, d.typ, arch)
		}
	}
	if len(l) != 6 || count[typeESP] != 1 || count[store.data] != 2 || count[store.verity] != 2 || count[storage.PartitionType(stateLabel)] != 1 {
		return nil, errors.New("the image's definitions do not lay out an ESP, two store slots and STATE, and nothing else")
	}
	return l, nil
}

// parseDefinition reads one definition of the system region, which must hold a [Partition]
// section alone, of an ESP, a store slot's partition or STATE, with no keys but those
// definitionKeys allows for it, each once.
func parseDefinition(name, text string) (definedPartition, error) {
	if strings.ContainsRune(name, '/') || !strings.HasSuffix(name, ".conf") {
		return definedPartition{}, fmt.Errorf("invalid definition file name %q", name)
	}
	d := definedPartition{file: name}
	values := map[string]string{}
	var keys []string
	section := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";"):
			continue
		case strings.HasSuffix(line, "\\"):
			return definedPartition{}, fmt.Errorf("%s continues a line, which a definition of the system region does not", name)
		case line == "[Partition]" && !section:
			section = true
			continue
		case strings.HasPrefix(line, "["):
			return definedPartition{}, fmt.Errorf("%s has the section %s; a definition of the system region holds one [Partition] section alone", name, line)
		}
		k, v, ok := strings.Cut(line, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch {
		case !ok:
			return definedPartition{}, fmt.Errorf("%s: %q is no key=value line", name, line)
		case !section:
			return definedPartition{}, fmt.Errorf("%s sets %s outside the [Partition] section", name, k)
		}
		if _, ok := values[k]; ok {
			return definedPartition{}, fmt.Errorf("%s sets %s twice", name, k)
		}
		values[k] = v
		keys = append(keys, k)
	}
	typ := strings.ToLower(values["Type"])
	if t, ok := partitionTypes[typ]; ok {
		typ = t
	}
	var kind string
	switch typ {
	case "":
		return definedPartition{}, fmt.Errorf("%s defines no partition type", name)
	case typeESP:
		kind = "esp"
	case typeUsrX86, typeUsrX86Verity, typeUsrArm, typeUsrArmVerity:
		kind = "store"
	case storage.PartitionType(stateLabel):
		kind = "state"
	default:
		return definedPartition{}, fmt.Errorf("%s: the partition type %q is none of the system region", name, values["Type"])
	}
	for _, k := range keys {
		if !definitionKeys[kind][k] {
			return definedPartition{}, fmt.Errorf("%s sets %s, which a definition of the system region's %s partition may not", name, k, kind)
		}
	}
	d.typ, d.label, d.format = typ, values["Label"], values["Format"]
	switch {
	case kind == "esp" && d.format != "vfat":
		return definedPartition{}, fmt.Errorf("%s does not format the ESP as vfat", name)
	case kind == "state" && d.label != stateLabel:
		return definedPartition{}, fmt.Errorf("%s does not label STATE %q", name, stateLabel)
	case kind == "state" && d.format != "ext4":
		return definedPartition{}, fmt.Errorf("%s does not format STATE as ext4", name)
	}
	var minSize, maxSize int64
	for _, s := range []struct {
		key  string
		size *int64
	}{{"SizeMinBytes", &minSize}, {"SizeMaxBytes", &maxSize}} {
		v, ok := values[s.key]
		if !ok {
			continue
		}
		var err error
		if *s.size, err = parseSize(v); err != nil {
			return definedPartition{}, fmt.Errorf("%s: %s: %w", name, s.key, err)
		}
	}
	// repart rounds the minimum up to a multiple of 4096 and the maximum down.
	minSize = (minSize + grain - 1) / grain * grain
	maxSize = maxSize / grain * grain
	if minSize > 0 && maxSize > 0 && minSize > maxSize {
		return definedPartition{}, fmt.Errorf("%s: SizeMinBytes rounds up to %d bytes, beyond SizeMaxBytes, which rounds down to %d; repart refuses it", name, minSize, maxSize)
	}
	d.maxSize = maxSize
	if minSize > 0 && minSize == maxSize {
		d.size = minSize
	}
	return d, nil
}

// grain is what repart rounds partition sizes to.
const grain = 4096

// sizeUnits are the units repart's sizes take, largest first, as systemd's parse_size reads them
// to the base of 1024; a number without one counts bytes.
var sizeUnits = []struct {
	suffix string
	factor uint64
}{{"E", 1 << 60}, {"P", 1 << 50}, {"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1}, {"", 1}}

// parseSize reads a size as repart does: one or more numbers, each with an optional fraction
// and a unit smaller than the one before, such as 4096, 1.5G, 512B or 1G 512M.
func parseSize(s string) (int64, error) {
	bad := fmt.Errorf("%q is not a size", s)
	var total uint64
	rest, next := s, 0
	for {
		rest = strings.TrimLeft(rest, " \t\n\r")
		rest = strings.TrimPrefix(rest, "+")
		digits := len(rest) - len(strings.TrimLeft(rest, "0123456789"))
		if digits == 0 {
			return 0, bad
		}
		n, err := strconv.ParseUint(rest[:digits], 10, 64)
		if err != nil {
			return 0, bad
		}
		rest = rest[digits:]
		var frac float64
		if after, ok := strings.CutPrefix(rest, "."); ok {
			rest = after
			digits := len(rest) - len(strings.TrimLeft(rest, "0123456789"))
			if digits > 0 {
				f, err := strconv.ParseUint(rest[:digits], 10, 64)
				if err != nil {
					return 0, bad
				}
				frac = float64(f) / math.Pow10(digits)
				rest = rest[digits:]
			}
		}
		rest = strings.TrimLeft(rest, " \t\n\r")
		unit := next
		for unit < len(sizeUnits) && !strings.HasPrefix(rest, sizeUnits[unit].suffix) {
			unit++
		}
		if unit == len(sizeUnits) {
			return 0, bad
		}
		factor := sizeUnits[unit].factor
		if n >= math.MaxInt64/factor {
			return 0, bad
		}
		part := n*factor + uint64(frac*float64(factor))
		if total > math.MaxInt64-part {
			return 0, bad
		}
		total += part
		rest = rest[len(sizeUnits[unit].suffix):]
		next = unit + 1
		if rest == "" {
			return int64(total), nil
		}
	}
}

// check reports how the disk's partitions differ from those the layout and the node's system
// disk volumes make: first the layout's partitions, of their types and order, with their labels
// and sizes where the definitions set them, then nothing but volumes of the system disk, which
// installing creates once STATE is open.
func (l layout) check(t partitionTable, section storage.Section) error {
	if len(t.Partitions) < len(l) {
		return fmt.Errorf("it carries %s, fewer partitions than the image's %d", describeAll(t.Partitions), len(l))
	}
	for n, d := range l {
		p := t.Partitions[n]
		switch {
		case p.Type != d.typ:
			return fmt.Errorf("its partition %d (%s) is not of the type %s that %s gives", p.Number, describe(p), d.typ, d.file)
		case d.label != "" && p.Name != d.label:
			return fmt.Errorf("its partition %d (%s) is not labelled %q as %s says", p.Number, describe(p), d.label, d.file)
		case d.size != 0 && p.Bytes() != d.size:
			return fmt.Errorf("its partition %d (%s) has %d bytes, not the %d of %s", p.Number, describe(p), p.Bytes(), d.size, d.file)
		}
	}
	volumes := map[string]bool{}
	for _, v := range section.Volumes {
		if v.Disk == storage.SystemDisk {
			volumes[v.Label] = true
		}
	}
	for _, p := range t.Partitions[len(l):] {
		if !volumes[p.Name] || p.Type != storage.PartitionType(p.Name) {
			return fmt.Errorf("its partition %d (%s) is no volume of the node's system disk", p.Number, describe(p))
		}
	}
	return nil
}

// describeAll names the partitions found on a disk.
func describeAll(parts []partition) string {
	if len(parts) == 0 {
		return "no partitions"
	}
	var all []string
	for _, p := range parts {
		all = append(all, fmt.Sprintf("partition %d (%s)", p.Number, describe(p)))
	}
	return strings.Join(all, ", ")
}

// fits refuses an image whose store or hash tree is larger than the definitions make slot A.
func (l layout) fits(h upgrade.Header) error {
	var verity, data *definedPartition
	for n, d := range l {
		switch {
		case verity == nil && (d.typ == typeUsrX86Verity || d.typ == typeUsrArmVerity):
			verity = &l[n]
		case data == nil && (d.typ == typeUsrX86 || d.typ == typeUsrArm):
			data = &l[n]
		}
	}
	for _, part := range []struct {
		what string
		d    *definedPartition
		size int64
	}{{"store", data, h.StoreSize}, {"hash tree", verity, h.VeritySize}} {
		if part.d.maxSize > 0 && part.size > part.d.maxSize {
			return fmt.Errorf("the image's %s of %d bytes does not fit slot A's partition of %d bytes, which %s defines", part.what, part.size, part.d.maxSize, part.d.file)
		}
	}
	return nil
}
