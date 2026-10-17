package upgrade

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

// TestGeneration reads an entry's generation from its ID, with or without the tries counter the
// name had, in either case; a name of another image, or one that gives no canonical decimal,
// gives none.
func TestGeneration(t *testing.T) {
	for file, want := range map[string]uint64{
		"chalkos_1.efi":                    1,
		"chalkos_12.efi":                   12,
		"chalkos_3+2.efi":                  3,
		"chalkos_3+0-3.efi":                3,
		"CHALKOS_4.EFI":                    4,
		"chalkos_18446744073709551615.efi": math.MaxUint64,
		"chalkos_18446744073709551616.efi": 0,
		"chalkos.efi":                      0,
		"chalkos_0.efi":                    0,
		"chalkos_07.efi":                   0,
		"chalkos_+5.efi":                   0,
		"chalkos_0.2.0.efi":                0,
		"chalkos_2a.efi":                   0,
		"rescue_2.efi":                     0,
		"chalkos-installer_1.efi":          0,
	} {
		id, _, _ := parseEntryName(file)
		if got := entryGeneration(id, "chalkos"); got != want {
			t.Errorf("the generation of %s = %d, want %d", file, got, want)
		}
	}
}

// TestNextGeneration numbers an upgrade's entry one above the highest generation any name gives
// the image, whatever the UKI holds, and leaves out names that give none.
func TestNextGeneration(t *testing.T) {
	entry := func(file, imageID string) Entry {
		id, _, _ := parseEntryName(file)
		return Entry{File: file, ID: id, ImageID: imageID}
	}
	for _, tc := range []struct {
		name    string
		entries []Entry
		want    uint64
	}{
		{"no entry", nil, 1},
		{"one entry", []Entry{entry("chalkos_1.efi", "chalkos")}, 2},
		{"in any order", []Entry{entry("chalkos_9.efi", "chalkos"), entry("chalkos_10+3.efi", "chalkos"), entry("chalkos_2.efi", "chalkos")}, 11},
		{"a name without one", []Entry{entry("chalkos.efi", "chalkos"), entry("chalkos_3.efi", "chalkos")}, 4},
		{"another image's", []Entry{entry("rescue_7.efi", "rescue"), entry("chalkos_3.efi", "chalkos")}, 4},
		// systemd-boot orders the entry by its name, whatever it boots.
		{"an unreadable UKI", []Entry{entry("chalkos_8.efi", ""), entry("chalkos_3.efi", "chalkos")}, 9},
		{"another image's under the image's name", []Entry{entry("chalkos_8.efi", "rescue"), entry("chalkos_3.efi", "chalkos")}, 9},
		{"leading zeros", []Entry{entry("chalkos_0012.efi", ""), entry("chalkos_3.efi", "chalkos")}, 13},
		{"a huge generation", []Entry{entry("chalkos_18446744073709551614.efi", "chalkos")}, math.MaxUint64},
	} {
		got, err := nextGeneration(tc.entries, "chalkos")
		if err != nil || got != tc.want {
			t.Errorf("%s: the next generation = %d, %v, want %d", tc.name, got, err, tc.want)
		}
	}
	for _, file := range []string{"chalkos_18446744073709551615.efi", "chalkos_18446744073709551616.efi", "chalkos_000099999999999999999999999.efi"} {
		if got, err := nextGeneration([]Entry{entry(file, "")}, "chalkos"); err == nil || !strings.Contains(err.Error(), "too large") {
			t.Errorf("past %s: the next generation = %d, %v, want it refused", file, got, err)
		}
	}
}

// TestStagedEntry finds the entry that boots next: the image's highest generation, not the
// booted entry, with tries left.
func TestStagedEntry(t *testing.T) {
	entry := func(file string, generation uint64, left int) Entry {
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
		// systemd-boot boots an entry whose name is higher first, whatever its UKI holds.
		{"an unreadable UKI above", []Entry{booted, entry("chalkos_3+3.efi", 3, 3), {File: "chalkos_4.efi", ID: "chalkos_4.efi", TriesLeft: -1}}, ""},
		{"a generation past a uint64 above", []Entry{booted, entry("chalkos_3+3.efi", 3, 3), {File: "chalkos_99999999999999999999.efi", ID: "chalkos_99999999999999999999.efi", TriesLeft: -1}}, ""},
		// systemd-boot orders entries of one generation by no rule chalkd relies on.
		{"two names of the highest generation", []Entry{booted, entry("chalkos_06+3.efi", 0, 3), entry("chalkos_6+3.efi", 6, 3)}, ""},
		{"a lower generation twice", []Entry{booted, entry("chalkos_02+3.efi", 0, 3), entry("chalkos_3+3.efi", 3, 3)}, "chalkos_3+3.efi"},
	} {
		got, ok := StagedEntry(tc.entries, "chalkos", booted)
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

// TestTriesCounter reads the tries counter of a name as systemd-boot does, which takes a count
// past an int32 for no counter, and the whole name for the entry's ID.
func TestTriesCounter(t *testing.T) {
	for _, tc := range []struct {
		file, id   string
		left, done int
	}{
		{"chalkos_2+3.efi", "chalkos_2.efi", 3, 0},
		{"chalkos_2+1-2.efi", "chalkos_2.efi", 1, 2},
		{"chalkos_2+2147483647-2147483647.efi", "chalkos_2.efi", math.MaxInt32, math.MaxInt32},
		{"chalkos_2+2147483648.efi", "chalkos_2+2147483648.efi", -1, -1},
		{"chalkos_2+1-2147483648.efi", "chalkos_2+1-2147483648.efi", -1, -1},
		{"chalkos_2+99999999999999999999.efi", "chalkos_2+99999999999999999999.efi", -1, -1},
		{"chalkos_2.efi", "chalkos_2.efi", -1, -1},
	} {
		id, left, done := parseEntryName(tc.file)
		if id != tc.id || left != tc.left || done != tc.done {
			t.Errorf("%s: %s, %d, %d; want %s, %d, %d", tc.file, id, left, done, tc.id, tc.left, tc.done)
		}
	}
}
