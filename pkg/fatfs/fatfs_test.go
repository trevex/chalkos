package fatfs

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// offset puts the file system after room for a partition table, as an ESP is.
const offset = 1 << 20

// disk makes a sparse file of offset plus size bytes, filled with junk where the file system
// goes when junk is set, as a reused disk would be.
func disk(t *testing.T, size int64, junk bool) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "disk.raw")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(offset + size + 1<<20); err != nil {
		t.Fatal(err)
	}
	if junk {
		block := bytes.Repeat([]byte{0xe5, 0x41, 0xff, 0x00, 0x2e, 0x10, 0x7f, 0x13}, 1<<14)
		for o := int64(0); o < size; o += int64(len(block)) {
			if _, err := f.WriteAt(block, offset+o); err != nil {
				t.Fatal(err)
			}
		}
	}
	return name
}

// content is n bytes that differ from those of other seeds.
func content(seed string, n int) []byte {
	return bytes.Repeat([]byte(seed), n/len(seed)+1)[:n]
}

// write formats the file system and writes files onto it in the order of their paths, making
// their directories.
func write(t *testing.T, name string, size int64, files map[string][]byte) {
	t.Helper()
	if err := Format(name, offset, size, "esp", 0x793a442b); err != nil {
		t.Fatal(err)
	}
	fsys, err := Open(name, offset, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range slices.Sorted(maps.Keys(files)) {
		if err := fsys.MkdirAll(filepath.Dir(p)); err != nil {
			t.Fatal(err)
		}
		if err := fsys.WriteFile(p, files[p]); err != nil {
			t.Fatal(err)
		}
	}
	if err := fsys.Close(); err != nil {
		t.Fatal(err)
	}
}

// read opens the file system read-only and returns the content of every file below /.
func read(t *testing.T, name string) map[string][]byte {
	t.Helper()
	fsys, err := Open(name, offset, false)
	if err != nil {
		t.Fatal(err)
	}
	defer fsys.Close()
	files, err := fsys.List("/")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]byte{}
	for _, p := range files {
		if got[p], err = fsys.ReadFile(p); err != nil {
			t.Fatal(err)
		}
	}
	return got
}

// TestWriteRead writes an ESP's files, with long and mixed-case names, onto a file system of an
// ESP's smallest size and of a test's, over zeros and over junk, and reads them back.
func TestWriteRead(t *testing.T) {
	files := map[string][]byte{
		"/EFI/BOOT/BOOTX64.EFI":    content("loader", 200<<10),
		"/EFI/Linux/chalkos_1.efi": content("uki", 3<<20+17),
		"/EFI/Linux/empty.efi":     nil,
		"/loader/entries.srel":     []byte("type1\n"),
	}
	for _, size := range []int64{260 << 20, 4 << 20} {
		for _, junk := range []bool{false, true} {
			t.Run(fmt.Sprintf("%dMiB/junk=%v", size>>20, junk), func(t *testing.T) {
				name := disk(t, size, junk)
				write(t, name, size, files)
				got := read(t, name)
				for p, data := range got {
					if !bytes.Equal(data, files[p]) {
						t.Errorf("%s holds %d bytes, not the %d written", p, len(data), len(files[p]))
					}
				}
				if len(got) != len(files) {
					t.Errorf("listed %d files, want %d", len(got), len(files))
				}
				checkTools(t, name, size, files)
			})
		}
	}
}

// TestFormatIsReproducible formats a new disk and writes the same files onto it twice, into the
// same bytes, with the label and the volume ID asked for.
func TestFormatIsReproducible(t *testing.T) {
	const size = 260 << 20
	files := map[string][]byte{"/EFI/BOOT/BOOTX64.EFI": content("loader", 1000), "/EFI/Linux/chalkos_1.efi": content("uki", 5000)}
	var sums [2][32]byte
	for i := range sums {
		name := disk(t, size, false)
		write(t, name, size, files)
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sums[i] = sha256.Sum256(data)
		boot := data[offset : offset+512]
		if string(boot[0x52:0x5a]) != "FAT32   " || binary.LittleEndian.Uint32(boot[0x43:]) != 0x793a442b || string(boot[0x47:0x52]) != "ESP        " {
			t.Errorf("the boot sector is of type %q, volume ID %x and label %q", boot[0x52:0x5a], boot[0x43:0x47], boot[0x47:0x52])
		}
		if backup := data[offset+6*512 : offset+7*512]; !bytes.Equal(backup, boot) {
			t.Error("the backup boot sector differs from the boot sector")
		}
	}
	if sums[0] != sums[1] {
		t.Errorf("the same files wrote disks of SHA-256 %x and %x", sums[0], sums[1])
	}
}

// TestReplace replaces files in place with longer and shorter content, as signing a UKI does,
// and leaves the other files and the bytes around the file system as they were.
func TestReplace(t *testing.T) {
	const size = 260 << 20
	name := disk(t, size, false)
	files := map[string][]byte{
		"/EFI/BOOT/BOOTX64.EFI":    content("loader", 200<<10),
		"/EFI/Linux/chalkos_1.efi": content("uki", 3<<20),
		"/EFI/tools/shell.efi":     content("shell", 4096),
	}
	write(t, name, size, files)
	before, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := Open(name, offset, true)
	if err != nil {
		t.Fatal(err)
	}
	// Names are case-insensitive.
	files["/EFI/Linux/chalkos_1.efi"] = append(files["/EFI/Linux/chalkos_1.efi"], content("signature", 4096)...)
	files["/EFI/BOOT/BOOTX64.EFI"] = content("short", 1000)
	for p, upper := range map[string]string{"/EFI/Linux/chalkos_1.efi": "/efi/LINUX/CHALKOS_1.EFI", "/EFI/BOOT/BOOTX64.EFI": "/efi/boot/bootx64.efi"} {
		if err := fsys.WriteFile(upper, files[p]); err != nil {
			t.Fatal(err)
		}
	}
	if err := fsys.Close(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, name); !reflect.DeepEqual(got, files) {
		t.Error("the files read back differ from those written")
	}
	after, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before[:offset], after[:offset]) || !bytes.Equal(before[offset+size:], after[offset+size:]) {
		t.Error("replacing files changed bytes outside the file system")
	}
	checkTools(t, name, size, files)
}

// TestRefusals refuses a file where a directory goes, a missing file and a file system that
// Open cannot read.
func TestRefusals(t *testing.T) {
	const size = 64 << 20
	name := disk(t, size, false)
	write(t, name, size, map[string][]byte{"/EFI/BOOT": nil})
	fsys, err := Open(name, offset, false)
	if err != nil {
		t.Fatal(err)
	}
	defer fsys.Close()
	if err := fsys.MkdirAll("/EFI/BOOT/x"); err == nil || !strings.Contains(err.Error(), "a file has its name") {
		t.Errorf("a directory over a file: %v", err)
	}
	if _, err := fsys.ReadFile("/EFI/missing.efi"); err == nil {
		t.Error("read a missing file")
	}
	if err := fsys.WriteFile("/EFI/x.efi", nil); err == nil {
		t.Error("wrote to a file system opened read-only")
	}
	if _, err := Open(name, 0, false); err == nil || !strings.Contains(err.Error(), "the FAT file system at 0") {
		t.Errorf("opened a file system where there is none: %v", err)
	}
}

// checkTools checks the file system with fsck.vfat and reads it with mtools, when they are on
// PATH: fsck finds nothing to fix, and mtools sees the label, the volume ID and the files.
func checkTools(t *testing.T, name string, size int64, files map[string][]byte) {
	t.Helper()
	for _, tool := range []string{"fsck.vfat", "mdir", "mcopy"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Logf("%s not in PATH: the file system is not checked against dosfstools and mtools", tool)
			return
		}
	}
	// fsck.vfat checks a whole file, so it gets a copy of the file system's bytes.
	in, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	region := filepath.Join(t.TempDir(), "fs.img")
	out, err := os.Create(region)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, io.NewSectionReader(in, offset, size)); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("fsck.vfat", "-n", region).CombinedOutput(); err != nil {
		t.Errorf("fsck.vfat -n: %v\n%s", err, out)
	}
	fat := fmt.Sprintf("%s@@%d", name, offset)
	mtools := func(args ...string) []byte {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = append(os.Environ(), "MTOOLS_SKIP_CHECK=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	if out := string(mtools("mdir", "-i", fat, "::/")); !strings.Contains(out, "Volume Serial Number is 793A-442B") {
		t.Errorf("mdir shows another volume ID:\n%s", out)
	}
	for p, data := range files {
		copied := filepath.Join(t.TempDir(), "copy")
		mtools("mcopy", "-n", "-i", fat, "::"+p, copied)
		if got, err := os.ReadFile(copied); err != nil || !bytes.Equal(got, data) {
			t.Errorf("mcopy reads %d bytes of %s, not the %d written: %v", len(got), p, len(data), err)
		}
	}
}
