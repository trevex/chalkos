package upgrade

import (
	"bytes"
	"testing"
)

// TestGeneration reads an entry's generation from its ID, with or without the tries counter the
// name had, in either case; a name of another image, or one that gives no canonical decimal,
// gives none.
func TestGeneration(t *testing.T) {
	for file, want := range map[string]int{
		"chalkos_1.efi":           1,
		"chalkos_12.efi":          12,
		"chalkos_3+2.efi":         3,
		"chalkos_3+0-3.efi":       3,
		"CHALKOS_4.EFI":           4,
		"chalkos.efi":             0,
		"chalkos_0.efi":           0,
		"chalkos_07.efi":          0,
		"chalkos_+5.efi":          0,
		"chalkos_0.2.0.efi":       0,
		"chalkos_2a.efi":          0,
		"rescue_2.efi":            0,
		"chalkos-installer_1.efi": 0,
	} {
		id, _, _ := parseEntryName(file)
		if got := entryGeneration(id, "chalkos"); got != want {
			t.Errorf("the generation of %s = %d, want %d", file, got, want)
		}
	}
}

// TestNextGeneration numbers an upgrade's entry one above the image's highest generation, leaving
// out UKIs of other images and names that give no generation.
func TestNextGeneration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []Entry
		want    int
	}{
		{"no entry", nil, 1},
		{"one entry", []Entry{{ImageID: "chalkos", Generation: 1}}, 2},
		{"in any order", []Entry{{ImageID: "chalkos", Generation: 9}, {ImageID: "chalkos", Generation: 10}, {ImageID: "chalkos", Generation: 2}}, 11},
		{"a name without one", []Entry{{File: "chalkos.efi", ImageID: "chalkos"}, {ImageID: "chalkos", Generation: 3}}, 4},
		{"another image's", []Entry{{File: "rescue_7.efi", ImageID: "rescue", Generation: 7}, {ImageID: "chalkos", Generation: 3}}, 4},
		{"an unreadable UKI", []Entry{{File: "chalkos_8.efi"}, {ImageID: "chalkos", Generation: 3}}, 4},
	} {
		if got := nextGeneration(tc.entries, "chalkos"); got != tc.want {
			t.Errorf("%s: the next generation = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestStagedEntry finds the entry that boots next: the image's highest generation, not the
// booted entry, with tries left.
func TestStagedEntry(t *testing.T) {
	entry := func(file string, generation, left int) Entry {
		id, _, _ := parseEntryName(file)
		return Entry{File: file, ID: id, ImageID: "chalkos", Generation: generation, TriesLeft: left}
	}
	booted := entry("chalkos_2.efi", 2, -1)
	for _, tc := range []struct {
		name    string
		entries []Entry
		want    string
	}{
		{"staged", []Entry{booted, entry("chalkos_3+3.efi", 3, 3)}, "chalkos_3+3.efi"},
		{"counted once", []Entry{booted, entry("chalkos_3+2-1.efi", 3, 2)}, "chalkos_3+2-1.efi"},
		{"its tries used up", []Entry{booted, entry("chalkos_3+0-3.efi", 3, 0)}, ""},
		{"the booted entry the highest", []Entry{entry("chalkos_1.efi", 1, -1), booted}, ""},
		{"a blessed entry above the booted one", []Entry{booted, entry("chalkos_3.efi", 3, -1)}, ""},
		{"another image's above", []Entry{booted, {File: "rescue_9+3.efi", ID: "rescue_9.efi", ImageID: "rescue", Generation: 9, TriesLeft: 3}}, ""},
	} {
		got, ok := stagedEntry(tc.entries, "chalkos", booted)
		if got.File != tc.want || ok != (tc.want != "") {
			t.Errorf("%s: staged %q, %v, want %q", tc.name, got.File, ok, tc.want)
		}
	}
}

// TestEntryNames names an image's UKIs by generation, with the tries counter of an upgrade or
// without one.
func TestEntryNames(t *testing.T) {
	if got := EntryName("chalkos", 1); got != "chalkos_1.efi" {
		t.Errorf("EntryName = %s", got)
	}
	if got := countedEntryName("chalkos", 12, 3); got != "chalkos_12+3.efi" {
		t.Errorf("countedEntryName = %s", got)
	}
}

// TestSlotLabels names a slot's store by its root hash's first 16 hex digits, and reads that ID
// back from a slot's labels, which retired and foreign slots do not carry.
func TestSlotLabels(t *testing.T) {
	root := bytes.Repeat([]byte{0x3f, 0xa9}, 16)
	if got := LabelID(root); got != "3fa93fa93fa93fa9" {
		t.Fatalf("LabelID = %s", got)
	}
	for _, tc := range []struct {
		verity, data, want string
	}{
		{"store-verity_3fa93fa93fa93fa9", "store_3fa93fa93fa93fa9", "3fa93fa93fa93fa9"},
		{"_empty", "_empty", ""},
		{"store-verity_3fa93fa93fa93fa9", "store_81bd02c4a9e1aa00", ""},
		{"store-verity", "store", ""},
	} {
		s := Slot{Verity: Partition{Name: tc.verity}, Data: Partition{Name: tc.data}}
		if got := s.Label(); got != tc.want {
			t.Errorf("the label of %s and %s = %q, want %q", tc.verity, tc.data, got, tc.want)
		}
	}
}
