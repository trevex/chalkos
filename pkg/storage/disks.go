package storage

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Host finds the node's disks through sysfs, the udev database and /dev. Tests point the roots
// at fixture trees.
type Host struct {
	SysRoot  string // /sys
	UdevRoot string // /run/udev/data
	DevRoot  string // /dev
}

// DefaultHost reads the running system.
func DefaultHost() Host {
	return Host{SysRoot: "/sys", UdevRoot: "/run/udev/data", DevRoot: "/dev"}
}

// Identity is what a physical disk is recognised by, independent of its device name.
type Identity struct {
	WWN    string `json:"wwn,omitempty"`
	Serial string `json:"serial,omitempty"`
	Model  string `json:"model,omitempty"`
	// Path is udev's ID_PATH, the only identity of disks without WWN and serial.
	Path string `json:"path,omitempty"`
	Size uint64 `json:"size"`
	Type string `json:"type"`
}

// Same reports whether two identities describe the same physical disk, by the strongest
// identifier both of them have. A disk whose udev entry gained a WWN after it was pinned is still
// the pinned disk. The path only decides when one side has no WWN or serial at all: two disks
// that each have a WWN or serial the other lacks share nothing that tells them apart.
func (a Identity) Same(b Identity) bool {
	switch {
	case a.WWN != "" && b.WWN != "":
		return a.WWN == b.WWN
	case a.Serial != "" && b.Serial != "":
		return a.Serial == b.Serial && a.Model == b.Model
	case a.unique() && b.unique():
		return false
	default:
		return a.Path != "" && a.Path == b.Path
	}
}

// unique reports whether the identity has an identifier that belongs to one disk only.
func (i Identity) unique() bool { return i.WWN != "" || i.Serial != "" }

// Update returns the pinned identity with what the disk found by it reports now, when the disk
// reports an identifier the pin lacks, so later boots match it by its strongest identifier.
// Otherwise the pin stays as it is; changed tells which.
func (i Identity) Update(found Identity) (updated Identity, changed bool) {
	if (i.WWN != "" || found.WWN == "") && (i.Serial != "" || found.Serial == "") && (i.Path != "" || found.Path == "") {
		return i, false
	}
	updated = found
	// Identifiers the disk no longer reports still identify it.
	if updated.WWN == "" {
		updated.WWN = i.WWN
	}
	if updated.Serial == "" {
		updated.Serial, updated.Model = i.Serial, i.Model
	}
	if updated.Path == "" {
		updated.Path = i.Path
	}
	return updated, true
}

// Recognisable reports whether the disk can be found again by this identity. Model, size and
// type are shared by identical disks, so they never identify one alone.
func (i Identity) Recognisable() bool {
	return i.WWN != "" || i.Serial != "" || i.Path != ""
}

func (i Identity) String() string {
	parts := []string{fmt.Sprintf("model %q", i.Model), "size " + formatSize(i.Size)}
	if i.Serial != "" {
		parts = append(parts, fmt.Sprintf("serial %q", i.Serial))
	}
	if i.WWN != "" {
		parts = append(parts, "wwn "+i.WWN)
	}
	if i.Serial == "" && i.WWN == "" && i.Path != "" {
		parts = append(parts, "path "+i.Path)
	}
	return strings.Join(parts, ", ")
}

// BlockDisk is a whole disk of the node.
type BlockDisk struct {
	// Name is the kernel name, such as nvme0n1.
	Name string
	// Device is the device node on the node, such as /dev/nvme0n1.
	Device   string
	Identity Identity
}

func (d BlockDisk) String() string { return d.Device + " (" + d.Identity.String() + ")" }

// Kernel names of block devices that are never disks a volume can live on.
var virtualPrefixes = []string{"loop", "ram", "zram", "dm-", "md", "sr", "nbd", "fd"}

// Disks lists the node's whole disks by kernel name.
func (h Host) Disks() ([]BlockDisk, error) {
	entries, err := os.ReadDir(filepath.Join(h.SysRoot, "block"))
	if err != nil {
		return nil, err
	}
	var disks []BlockDisk
	for _, e := range entries {
		name := e.Name()
		if hasAnyPrefix(name, virtualPrefixes) {
			continue
		}
		d, err := h.disk(name)
		if err != nil {
			return nil, fmt.Errorf("disk %s: %w", name, err)
		}
		if d.Identity.Size > 0 {
			disks = append(disks, d)
		}
	}
	sort.Slice(disks, func(i, j int) bool { return disks[i].Name < disks[j].Name })
	return disks, nil
}

func (h Host) disk(name string) (BlockDisk, error) {
	dir := filepath.Join(h.SysRoot, "block", name)
	devnum, err := readTrimmed(filepath.Join(dir, "dev"))
	if err != nil {
		return BlockDisk{}, err
	}
	sectors, err := readTrimmed(filepath.Join(dir, "size"))
	if err != nil {
		return BlockDisk{}, err
	}
	n, err := strconv.ParseUint(sectors, 10, 64)
	if err != nil {
		return BlockDisk{}, fmt.Errorf("size: %w", err)
	}
	props, err := readUdevProperties(filepath.Join(h.UdevRoot, "b"+devnum))
	if err != nil {
		return BlockDisk{}, err
	}

	id := Identity{
		WWN:    props["ID_WWN"],
		Serial: props["ID_SERIAL_SHORT"],
		Model:  strings.ReplaceAll(props["ID_MODEL"], "_", " "),
		Path:   props["ID_PATH"],
		// sysfs counts 512-byte sectors regardless of the disk's block size.
		Size: n * 512,
	}
	if id.Serial == "" {
		id.Serial = props["ID_SERIAL"]
	}
	if id.Model == "" {
		id.Model, _ = readTrimmed(filepath.Join(dir, "device", "model"))
	}
	rotational, _ := readTrimmed(filepath.Join(dir, "queue", "rotational"))
	switch {
	case strings.HasPrefix(name, "nvme"):
		id.Type = "nvme"
	case rotational == "0":
		id.Type = "ssd"
	default:
		id.Type = "hdd"
	}
	return BlockDisk{Name: name, Device: "/dev/" + name, Identity: id}, nil
}

// Resolve finds the one disk a reference names.
func (h Host) Resolve(ref Ref) (BlockDisk, error) {
	if ref.Path != "" {
		return h.ResolvePath(ref.Path)
	}
	disks, err := h.Disks()
	if err != nil {
		return BlockDisk{}, err
	}
	var matches []BlockDisk
	for _, d := range disks {
		ok, err := ref.Selector.Matches(d.Identity)
		if err != nil {
			return BlockDisk{}, err
		}
		if ok {
			matches = append(matches, d)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return BlockDisk{}, fmt.Errorf("no disk matches %s; disks: %s", ref, listDisks(disks))
	default:
		return BlockDisk{}, fmt.Errorf("%d disks match %s, refusing to choose: %s", len(matches), ref, listDisks(matches))
	}
}

// ResolvePath finds the disk a /dev path names, following symlinks such as /dev/disk/by-id/....
func (h Host) ResolvePath(p string) (BlockDisk, error) {
	rel, ok := strings.CutPrefix(p, "/dev/")
	if !ok {
		return BlockDisk{}, fmt.Errorf("%s is not below /dev", p)
	}
	root, err := filepath.EvalSymlinks(h.DevRoot)
	if err != nil {
		return BlockDisk{}, err
	}
	target, err := filepath.EvalSymlinks(filepath.Join(root, rel))
	if err != nil {
		return BlockDisk{}, fmt.Errorf("%s: %w", p, err)
	}
	if filepath.Dir(target) != root {
		return BlockDisk{}, fmt.Errorf("%s points to %s, which is not a disk", p, target)
	}
	disks, err := h.Disks()
	if err != nil {
		return BlockDisk{}, err
	}
	for _, d := range disks {
		if d.Name == filepath.Base(target) {
			return d, nil
		}
	}
	return BlockDisk{}, fmt.Errorf("%s is %s, which is not a whole disk", p, "/dev/"+filepath.Base(target))
}

// Find returns the disk with a pinned identity; ok is false when no disk has it. Several disks
// with the identity are an error: picking one could open another disk's partitions.
func (h Host) Find(id Identity) (disk BlockDisk, ok bool, err error) {
	disks, err := h.Disks()
	if err != nil {
		return BlockDisk{}, false, err
	}
	var matches []BlockDisk
	for _, d := range disks {
		if d.Identity.Same(id) {
			matches = append(matches, d)
		}
	}
	switch len(matches) {
	case 0:
		return BlockDisk{}, false, nil
	case 1:
		return matches[0], true, nil
	default:
		return BlockDisk{}, false, fmt.Errorf("%d disks have the pinned identity %s, refusing to choose: %s", len(matches), id, listDisks(matches))
	}
}

// Matches reports whether a disk has every property the selector names.
func (s Selector) Matches(id Identity) (bool, error) {
	if s.unknown != "" {
		return false, fmt.Errorf("the selector uses %s, which this version of chalkos does not know", s.unknown)
	}
	if s.Model != "" {
		ok, err := path.Match(s.Model, id.Model)
		if err != nil {
			return false, fmt.Errorf("model pattern %q: %w", s.Model, err)
		}
		if !ok {
			return false, nil
		}
	}
	if s.Serial != "" && s.Serial != id.Serial {
		return false, nil
	}
	if s.WWN != "" && normalizeWWN(s.WWN) != normalizeWWN(id.WWN) {
		return false, nil
	}
	if s.Type != "" && s.Type != id.Type {
		return false, nil
	}
	if s.Size != "" {
		return matchSize(s.Size, id.Size)
	}
	return true, nil
}

func listDisks(disks []BlockDisk) string {
	if len(disks) == 0 {
		return "none"
	}
	items := make([]string, len(disks))
	for i, d := range disks {
		items[i] = d.String()
	}
	return strings.Join(items, "; ")
}

func formatSize(bytes uint64) string {
	units := []string{"B", "K", "M", "G", "T", "P"}
	value := float64(bytes)
	i := 0
	for value >= 1024 && i < len(units)-1 {
		value /= 1024
		i++
	}
	return strings.TrimSuffix(strconv.FormatFloat(value, 'f', 1, 64), ".0") + units[i]
}

// readUdevProperties reads the E: lines of a udev database entry; a missing entry has none.
func readUdevProperties(path string) (map[string]string, error) {
	props := map[string]string{}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return props, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if kv, ok := strings.CutPrefix(sc.Text(), "E:"); ok {
			if k, v, ok := strings.Cut(kv, "="); ok {
				props[k] = v
			}
		}
	}
	return props, sc.Err()
}

func readTrimmed(path string) (string, error) {
	data, err := os.ReadFile(path)
	return strings.TrimSpace(string(data)), err
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// normalizeWWN drops the 0x prefix and the case, so a WWN copied from another tool matches udev's.
func normalizeWWN(wwn string) string {
	wwn = strings.ToLower(wwn)
	return strings.TrimPrefix(wwn, "0x")
}
