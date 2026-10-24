package install

import (
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/gpt"
)

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{
		"4096": 4096, "256K": 256 << 10, "128M": 128 << 20, "3G": 3 << 30, "1T": 1 << 40,
		"512B": 512, "1.5G": 3 << 29, "1G 512M": 3 << 29, "1.25K": 1280, "2 M": 2 << 20, "1.5": 1, "0.0001K": 0,
	} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v, want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "-1M", "1Q", "M", "1M 1G", "1G1G", "1KB", ".5G", "20E", "1G "} {
		if _, err := parseSize(in); err == nil {
			t.Errorf("parseSize(%q) took it", in)
		}
	}
}

// TestParseLayoutRounds rounds sizes as repart does: SizeMinBytes up to a multiple of 4096 and
// SizeMaxBytes down, so a partition is fixed when both round to the same size.
func TestParseLayoutRounds(t *testing.T) {
	defs := func(store string) map[string]string {
		d := maps.Clone(labDefinitions)
		d["20-store-a.conf"] = "[Partition]\nType=usr-x86-64\n" + store
		return d
	}
	for _, tc := range []struct {
		store string
		size  int64
		want  string
	}{
		{"SizeMinBytes=2M\nSizeMaxBytes=2M\n", 2 << 20, ""},
		{"SizeMinBytes=2096000\nSizeMaxBytes=2M\n", 2 << 20, ""},
		{"SizeMinBytes=2096000\nSizeMaxBytes=2100000\n", 2 << 20, ""},
		{"SizeMinBytes=1M\nSizeMaxBytes=2M\n", 0, ""},
		{"SizeMinBytes=2097200\nSizeMaxBytes=2097200\n", 0, "SizeMinBytes rounds up to 2101248 bytes, beyond SizeMaxBytes, which rounds down to 2097152"},
	} {
		l, err := parseLayout(defs(tc.store), "x86-64")
		if tc.want != "" {
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%q: %v, want %q", tc.store, err, tc.want)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if l[2].size != tc.size {
			t.Errorf("%q fixes %d bytes, want %d", tc.store, l[2].size, tc.size)
		}
	}
}

// TestLayoutCheck compares tables against the lab's definitions: the image's partitions in order,
// counted in the disk's sectors, then only volumes of the system disk.
func TestLayoutCheck(t *testing.T) {
	l, err := parseLayout(labDefinitions, "x86-64")
	if err != nil {
		t.Fatal(err)
	}
	section := testSection(storage.EncryptionNone, storage.FallbackNone, "/dev/vdb")
	region := func(sector int64) []gpt.Partition {
		return []gpt.Partition{
			{Number: 1, Type: gpt.TypeESP, Size: 260 << 20 / sector},
			{Number: 2, Type: gpt.TypeUsrX86Verity, Name: "store-verity_3fa93fa93fa93fa9", Size: 256 << 10 / sector},
			{Number: 3, Type: gpt.TypeUsrX86, Name: "store_3fa93fa93fa93fa9", Size: 2 << 20 / sector},
			{Number: 4, Type: gpt.TypeUsrX86Verity, Name: "_empty", Size: 256 << 10 / sector},
			{Number: 5, Type: gpt.TypeUsrX86, Name: "_empty", Size: 2 << 20 / sector},
			{Number: 6, Type: stateType, Name: "state", Size: 32 << 20 / sector},
		}
	}
	table := func(sector int64, edit func([]gpt.Partition) []gpt.Partition) partitionTable {
		parts := region(sector)
		if edit != nil {
			parts = edit(parts)
		}
		for n := range parts {
			parts[n].SectorSize = sector
		}
		return partitionTable{Partitions: parts}
	}
	for _, tc := range []struct {
		name   string
		sector int64
		edit   func([]gpt.Partition) []gpt.Partition
		want   string
	}{
		{"the region", 512, nil, ""},
		{"the region on a 4Kn disk", 4096, nil, ""},
		{"with VAR", 512, func(p []gpt.Partition) []gpt.Partition {
			return append(p, gpt.Partition{Number: 7, Type: varType, Name: "var", Size: 1000})
		}, ""},
		{"with another partition", 512, func(p []gpt.Partition) []gpt.Partition {
			return append(p, gpt.Partition{Number: 7, Type: "0fc63daf-8483-4772-8e79-3d69d8477de4", Name: "data", Size: 1000})
		}, "partition 7 (type 0fc63daf-8483-4772-8e79-3d69d8477de4, label \"data\") is no volume"},
		{"without STATE", 512, func(p []gpt.Partition) []gpt.Partition { return p[:5] }, "fewer partitions than the image's 6"},
		{"slot B labelled", 512, func(p []gpt.Partition) []gpt.Partition { p[3].Name = "store-verity_81bd02c4a9e1aa00"; return p }, "not labelled \"_empty\""},
		{"a larger slot", 512, func(p []gpt.Partition) []gpt.Partition { p[2].Size *= 2; return p }, "has 4194304 bytes, not the 2097152"},
		{"the slots swapped", 512, func(p []gpt.Partition) []gpt.Partition { p[1], p[2] = p[2], p[1]; return p }, "is not of the type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := l.check(table(tc.sector, tc.edit), section)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("check = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestParseLayoutTakesTheImageDefinitions takes the definitions the test image ships as they
// are, as chalkos builds them for every image, and writes them with STATE's encryption.
func TestParseLayoutTakesTheImageDefinitions(t *testing.T) {
	dir := os.Getenv("CHALKOS_TEST_IMAGE_DEFINITIONS")
	if dir == "" {
		t.Skip("CHALKOS_TEST_IMAGE_DEFINITIONS not set")
	}
	defs, err := readDefinitions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseLayout(defs, "x86-64"); err != nil {
		t.Fatal(err)
	}
	if err := writeDefinitions(t.TempDir(), defs, storage.EncryptionTPM2); err != nil {
		t.Fatal(err)
	}
}

// TestParseLayoutRefuses refuses definitions that lay out more or other partitions than an ESP,
// two store slots of the image's architecture and STATE, or that set any key the system region's
// own definitions do not, such as one that copies files or blocks from the installer onto the
// target disk. writeDefinitions refuses each definition with a key it may not set as well.
func TestParseLayoutRefuses(t *testing.T) {
	state := labDefinitions["50-state.conf"]
	for name, tc := range map[string]struct {
		file, text, want string
	}{
		"CopyFiles":                {"50-state.conf", state + "CopyFiles=/etc:/etc\n", "50-state.conf sets CopyFiles"},
		"CopyBlocks":               {"20-store-a.conf", labDefinitions["20-store-a.conf"] + "CopyBlocks=/dev/vda\n", "20-store-a.conf sets CopyBlocks"},
		"MakeDirectories":          {"50-state.conf", state + "MakeDirectories=/x\n", "sets MakeDirectories"},
		"Subvolumes":               {"50-state.conf", state + "Subvolumes=/x\n", "sets Subvolumes"},
		"ExcludeFiles":             {"50-state.conf", state + "ExcludeFiles=/x\n", "sets ExcludeFiles"},
		"an unknown key":           {"00-esp.conf", labDefinitions["00-esp.conf"] + "Frobnicate=yes\n", "00-esp.conf sets Frobnicate"},
		"Encrypt on a store":       {"20-store-a.conf", labDefinitions["20-store-a.conf"] + "Encrypt=key-file\n", "sets Encrypt, which a definition of the system region's store partition may not"},
		"Format on a store":        {"20-store-a.conf", labDefinitions["20-store-a.conf"] + "Format=ext4\n", "sets Format"},
		"a key set twice":          {"20-store-a.conf", labDefinitions["20-store-a.conf"] + "SizeMaxBytes=3M\n", "sets SizeMaxBytes twice"},
		"another section":          {"50-state.conf", state + "[Other]\nCopyFiles=/etc\n", "has the section [Other]"},
		"a second [Partition]":     {"50-state.conf", state + "[Partition]\n", "has the section [Partition]"},
		"a key before the section": {"00-esp.conf", "Type=esp\n" + labDefinitions["00-esp.conf"], "sets Type outside the [Partition] section"},
		"a line without =":         {"00-esp.conf", labDefinitions["00-esp.conf"] + "CopyFiles\n", "is no key=value line"},
		"a continued line":         {"50-state.conf", state + "Label=state \\\n", "continues a line"},
		"another partition":        {"60-var.conf", "[Partition]\nType=" + storage.PartitionType("var") + "\nLabel=var\n", "the partition type"},
		"a third store slot":       {"60-store-c.conf", labDefinitions["20-store-a.conf"], "two store slots and STATE, and nothing else"},
		"stores of another arch":   {"20-store-a.conf", "[Partition]\nType=usr-arm64\nSizeMinBytes=2M\nSizeMaxBytes=2M\n", "none of the system region of an image for x86-64"},
		"STATE of another label":   {"50-state.conf", strings.Replace(state, "Label=state", "Label=other", 1), `does not label STATE "state"`},
		"STATE of another format":  {"50-state.conf", strings.Replace(state, "Format=ext4", "Format=btrfs", 1), "does not format STATE as ext4"},
	} {
		t.Run(name, func(t *testing.T) {
			defs := maps.Clone(labDefinitions)
			defs[tc.file] = tc.text
			if _, err := parseLayout(defs, "x86-64"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("parseLayout = %v, want %q", err, tc.want)
			}
		})
	}
	if err := writeDefinitions(t.TempDir(), map[string]string{"50-state.conf": state + "CopyFiles=/etc:/etc\n"}, storage.EncryptionNone); err == nil || !strings.Contains(err.Error(), "sets CopyFiles") {
		t.Errorf("writeDefinitions = %v, want CopyFiles refused", err)
	}
}
