package install

import (
	"maps"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/storage"
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
		l, err := parseLayout(defs(tc.store))
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

// TestLayoutCheck compares tables against the lab's definitions: the role's partitions in order,
// counted in the disk's sectors, then only volumes of the system disk.
func TestLayoutCheck(t *testing.T) {
	l, err := parseLayout(labDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	section := testSection(storage.EncryptionNone, storage.FallbackNone, "/dev/vdb")
	region := func(sector int64) []partition {
		return []partition{
			{Number: 1, Type: typeESP, Size: 260 << 20 / sector},
			{Number: 2, Type: typeUsrX86Verity, Name: "store-verity_0.1.0", Size: 256 << 10 / sector},
			{Number: 3, Type: typeUsrX86, Name: "store_0.1.0", Size: 2 << 20 / sector},
			{Number: 4, Type: typeUsrX86Verity, Name: "_empty", Size: 256 << 10 / sector},
			{Number: 5, Type: typeUsrX86, Name: "_empty", Size: 2 << 20 / sector},
			{Number: 6, Type: stateType, Name: "state", Size: 32 << 20 / sector},
		}
	}
	table := func(sector int64, edit func([]partition) []partition) partitionTable {
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
		edit   func([]partition) []partition
		want   string
	}{
		{"the region", 512, nil, ""},
		{"the region on a 4Kn disk", 4096, nil, ""},
		{"with VAR", 512, func(p []partition) []partition {
			return append(p, partition{Number: 7, Type: varType, Name: "var", Size: 1000})
		}, ""},
		{"with another partition", 512, func(p []partition) []partition {
			return append(p, partition{Number: 7, Type: "0fc63daf-8483-4772-8e79-3d69d8477de4", Name: "data", Size: 1000})
		}, "partition 7 (type 0fc63daf-8483-4772-8e79-3d69d8477de4, label \"data\") is no volume"},
		{"without STATE", 512, func(p []partition) []partition { return p[:5] }, "fewer partitions than the role's 6"},
		{"slot B labelled", 512, func(p []partition) []partition { p[3].Name = "store-verity_0.2.0"; return p }, "not labelled \"_empty\""},
		{"a larger slot", 512, func(p []partition) []partition { p[2].Size *= 2; return p }, "has 4194304 bytes, not the 2097152"},
		{"the slots swapped", 512, func(p []partition) []partition { p[1], p[2] = p[2], p[1]; return p }, "is not of the type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := l.check(table(tc.sector, tc.edit), section)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("check = %v, want %q", err, tc.want)
			}
		})
	}
}
