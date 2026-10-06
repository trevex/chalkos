package storage

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// fixtureDisk is a disk as sysfs and the udev database describe it.
type fixtureDisk struct {
	name       string
	devnum     string
	sectors    uint64
	rotational string
	sysModel   string
	props      map[string]string
	// links are /dev paths, without the /dev/ prefix, that udev points at the disk.
	links []string
}

const tib = 1 << 40

var (
	nvmeSystem = fixtureDisk{
		name: "nvme0n1", devnum: "259:0", sectors: 2 * tib / 512, rotational: "0",
		sysModel: "Samsung SSD 990 PRO 2TB",
		props: map[string]string{
			"ID_MODEL":        "Samsung_SSD_990_PRO_2TB",
			"ID_SERIAL_SHORT": "S7KHNJ0W100002",
			"ID_WWN":          "eui.002538b241b10002",
			"ID_PATH":         "pci-0000:01:00.0-nvme-1",
		},
		links: []string{"disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100002", "disk/chalk-boot-disk"},
	}
	sataSSD = fixtureDisk{
		name: "sda", devnum: "8:0", sectors: 4 * tib / 512, rotational: "0",
		props: map[string]string{
			"ID_MODEL":        "Samsung_SSD_870_QVO_4TB",
			"ID_SERIAL_SHORT": "S6PFNX0T100001",
			"ID_PATH":         "pci-0000:00:17.0-ata-1",
		},
	}
	sataHDD = fixtureDisk{
		name: "sdb", devnum: "8:16", sectors: 8 * tib / 512, rotational: "1",
		props: map[string]string{
			"ID_MODEL":        "ST8000VN004-3CP101",
			"ID_SERIAL_SHORT": "WWZ1A2B3",
			"ID_PATH":         "pci-0000:00:17.0-ata-2",
		},
	}
	// virtio disks without a serial have nothing but their path.
	virtioDisk = fixtureDisk{
		name: "vdb", devnum: "253:16", sectors: 2 << 30 / 512, rotational: "1",
		props: map[string]string{"ID_PATH": "pci-0000:00:05.0"},
	}
	loopDevice = fixtureDisk{name: "loop0", devnum: "7:0", sectors: 2048, rotational: "1"}
)

func newHost(t *testing.T, disks ...fixtureDisk) Host {
	t.Helper()
	root := t.TempDir()
	h := Host{
		SysRoot:  filepath.Join(root, "sys"),
		UdevRoot: filepath.Join(root, "run", "udev", "data"),
		DevRoot:  filepath.Join(root, "dev"),
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range disks {
		dir := filepath.Join(h.SysRoot, "block", d.name)
		write(filepath.Join(dir, "dev"), d.devnum+"\n")
		write(filepath.Join(dir, "size"), strconv.FormatUint(d.sectors, 10)+"\n")
		write(filepath.Join(dir, "queue", "rotational"), d.rotational+"\n")
		if d.sysModel != "" {
			write(filepath.Join(dir, "device", "model"), d.sysModel+"   \n")
		}
		var lines []string
		for k, v := range d.props {
			lines = append(lines, "E:"+k+"="+v)
		}
		sort.Strings(lines)
		write(filepath.Join(h.UdevRoot, "b"+d.devnum), "I:1234\n"+strings.Join(lines, "\n")+"\n")
		node := filepath.Join(h.DevRoot, d.name)
		write(node, "")
		// A partition node, which is not a whole disk.
		write(filepath.Join(h.DevRoot, d.name+"1"), "")
		for _, link := range d.links {
			path := filepath.Join(h.DevRoot, link)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			target, err := filepath.Rel(filepath.Dir(path), node)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}
	}
	return h
}

func TestDisksReadsIdentities(t *testing.T) {
	h := newHost(t, nvmeSystem, sataSSD, sataHDD, virtioDisk, loopDevice)
	disks, err := h.Disks()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Identity{}
	for _, d := range disks {
		got[d.Device] = d.Identity
	}
	want := map[string]Identity{
		"/dev/nvme0n1": {WWN: "eui.002538b241b10002", Serial: "S7KHNJ0W100002", Model: "Samsung SSD 990 PRO 2TB", Path: "pci-0000:01:00.0-nvme-1", Size: 2 * tib, Type: "nvme"},
		"/dev/sda":     {Serial: "S6PFNX0T100001", Model: "Samsung SSD 870 QVO 4TB", Path: "pci-0000:00:17.0-ata-1", Size: 4 * tib, Type: "ssd"},
		"/dev/sdb":     {Serial: "WWZ1A2B3", Model: "ST8000VN004-3CP101", Path: "pci-0000:00:17.0-ata-2", Size: 8 * tib, Type: "hdd"},
		"/dev/vdb":     {Path: "pci-0000:00:05.0", Size: 2 << 30, Type: "hdd"},
	}
	if len(got) != len(want) {
		t.Errorf("disks = %v, want %v", got, want)
	}
	for dev, w := range want {
		if got[dev] != w {
			t.Errorf("%s = %+v, want %+v", dev, got[dev], w)
		}
	}
}

func TestResolveSelectorOneMatch(t *testing.T) {
	h := newHost(t, nvmeSystem, sataSSD, sataHDD)
	for _, sel := range []Selector{
		{Model: "Samsung SSD 870*", Type: "ssd"},
		{Serial: "S6PFNX0T100001"},
		{Type: "ssd"},
		{Size: ">= 3T", Type: "ssd"},
	} {
		d, err := h.Resolve(Ref{Selector: sel})
		if err != nil {
			t.Errorf("%+v: %v", sel, err)
			continue
		}
		if d.Device != "/dev/sda" {
			t.Errorf("%+v resolved to %s, want /dev/sda", sel, d.Device)
		}
	}
	d, err := h.Resolve(Ref{Selector: Selector{WWN: "EUI.002538B241B10002"}})
	if err != nil || d.Device != "/dev/nvme0n1" {
		t.Errorf("wwn resolved to %v, %v", d, err)
	}
}

func TestResolveSelectorNoMatch(t *testing.T) {
	h := newHost(t, nvmeSystem, sataSSD)
	_, err := h.Resolve(Ref{Selector: Selector{Model: "WDC*"}})
	if err == nil {
		t.Fatal("resolved a selector no disk matches")
	}
	for _, want := range []string{"no disk matches", `model "Samsung SSD 870 QVO 4TB"`, "size 4T", `serial "S6PFNX0T100001"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestResolveSelectorAmbiguous(t *testing.T) {
	h := newHost(t, nvmeSystem, sataSSD, sataHDD)
	_, err := h.Resolve(Ref{Selector: Selector{Model: "Samsung*"}})
	if err == nil || !strings.Contains(err.Error(), "2 disks match") ||
		!strings.Contains(err.Error(), "/dev/nvme0n1") || !strings.Contains(err.Error(), "/dev/sda") {
		t.Fatalf("err = %v, want both Samsung disks listed", err)
	}
}

func TestResolvePath(t *testing.T) {
	h := newHost(t, nvmeSystem, sataSSD)
	for _, p := range []string{"/dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7KHNJ0W100002", "/dev/nvme0n1", "/dev/disk/chalk-boot-disk"} {
		d, err := h.ResolvePath(p)
		if err != nil || d.Device != "/dev/nvme0n1" {
			t.Errorf("ResolvePath(%s) = %v, %v", p, d, err)
		}
	}
	for _, p := range []string{"/dev/sda1", "/dev/disk/by-id/missing", "/srv/disk"} {
		if d, err := h.ResolvePath(p); err == nil {
			t.Errorf("ResolvePath(%s) = %v, want an error", p, d)
		}
	}
}

func TestFindPinnedDisk(t *testing.T) {
	h := newHost(t, nvmeSystem, sataSSD, virtioDisk)
	// The disk moved to another controller port; its serial still identifies it.
	pinned := Identity{Serial: "S6PFNX0T100001", Model: "Samsung SSD 870 QVO 4TB", Path: "pci-0000:00:17.0-ata-4"}
	d, ok, err := h.Find(pinned)
	if err != nil || !ok || d.Device != "/dev/sda" {
		t.Errorf("Find(serial) = %v, %v, %v", d, ok, err)
	}
	d, ok, err = h.Find(Identity{Path: "pci-0000:00:05.0"})
	if err != nil || !ok || d.Device != "/dev/vdb" {
		t.Errorf("Find(path) = %v, %v, %v", d, ok, err)
	}
	if _, ok, err := h.Find(Identity{Serial: "GONE"}); ok || err != nil {
		t.Errorf("Find(missing) = %v, %v", ok, err)
	}
}

func TestIdentitySame(t *testing.T) {
	for _, c := range []struct {
		a, b Identity
		want bool
	}{
		{Identity{WWN: "w1", Serial: "s1"}, Identity{WWN: "w1", Serial: "other"}, true},
		{Identity{WWN: "w1"}, Identity{WWN: "w2"}, false},
		{Identity{Serial: "s1", Model: "m"}, Identity{Serial: "s1", Model: "m", Path: "elsewhere"}, true},
		{Identity{Serial: "s1", Model: "m"}, Identity{Serial: "s1", Model: "n"}, false},
		{Identity{Path: "p"}, Identity{Path: "p"}, true},
		{Identity{}, Identity{}, false},
	} {
		if got := c.a.Same(c.b); got != c.want {
			t.Errorf("%+v.Same(%+v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
