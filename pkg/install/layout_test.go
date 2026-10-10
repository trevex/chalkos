package install

import (
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/storage"
)

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"4096": 4096, "256K": 256 << 10, "128M": 128 << 20, "3G": 3 << 30, "1T": 1 << 40} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v, want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "1.5G", "-1M", "1Q", "M"} {
		if _, err := parseSize(in); err == nil {
			t.Errorf("parseSize(%q) took it", in)
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
			parts[n].Bytes = parts[n].Size * sector
		}
		return partitionTable{SectorSize: sector, Partitions: parts}
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
