package upgrade

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/unix"

	"github.com/trevex/chalkos/pkg/uki"
)

// linuxDir is where systemd-boot finds UKIs on the ESP.
const linuxDir = "EFI/Linux"

// tempPrefix starts the name a UKI is written under before it gets its own. systemd-boot
// ignores files whose names start with a dot.
const tempPrefix = ".upgrade-"

// Entry is a UKI on the ESP, as systemd-boot sees it.
type Entry struct {
	// File is its name in /EFI/Linux; ID the boot loader entry's ID, the name without the tries
	// counter in lower case.
	File, ID string
	// TriesLeft and TriesDone are the counter in the name; -1 without one, which marks an entry
	// whose boot was found good, or which was never counted.
	TriesLeft, TriesDone int
	// Readable is set when the file parses as a UKI. ImageID is the image ID its os-release names,
	// and RootHash the root hash of the store it boots; both are empty when it does not parse.
	Readable bool
	ImageID  string
	RootHash []byte
	// Generation numbers the image's entries, as the name <image ID>_<generation>.efi gives it; 0
	// for a name that gives none or a UKI that cannot be read.
	Generation uint64
}

// Bad reports whether systemd-boot used up the entry's tries.
func (e Entry) Bad() bool { return e.TriesLeft == 0 }

// nameGeneration reads the generation an entry ID names for the image: the image ID, an
// underscore and any decimal, in lower case. systemd-boot orders the entry by that name whatever
// the UKI holds, so the name alone counts. ok is false for an ID of another shape, and err is set
// when the decimal does not fit a uint64.
func nameGeneration(id, imageID string) (g uint64, ok bool, err error) {
	s, found := strings.CutPrefix(id, strings.ToLower(imageID)+"_")
	if !found {
		return 0, false, nil
	}
	if s, found = strings.CutSuffix(s, ".efi"); !found || s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false, nil
	}
	g, err = strconv.ParseUint(s, 10, 64)
	return g, true, err
}

// entryGeneration is the generation an entry's ID gives a UKI of the image when the ID names it as
// chalkos does: from 1 on, without leading zeros, so no two names give one generation and the
// generation orders the entry as systemd-boot's comparison of the name does. Any other name
// gives 0.
func entryGeneration(id, imageID string) uint64 {
	g, ok, err := nameGeneration(id, imageID)
	if !ok || err != nil || g < 1 || id != strings.ToLower(EntryName(imageID, g)) {
		return 0
	}
	return g
}

// nextGeneration is the generation an upgrade names the image's new entry: one above the highest
// any name on the ESP gives the image, also of a UKI that cannot be read or names another image.
// systemd-boot compares chalkos's entries by their IDs alone, the highest first, so the new entry
// boots next, whatever the others hold. It fails when a name gives a generation past a uint64's
// range, or the highest leaves no next one.
func nextGeneration(entries []Entry, imageID string) (uint64, error) {
	var highest uint64
	for _, e := range entries {
		g, ok, err := nameGeneration(e.ID, imageID)
		if !ok {
			continue
		}
		if err != nil || g == math.MaxUint64 {
			return 0, fmt.Errorf("%s on the ESP names a generation too large for another to follow; remove it, or reinstall the node", e.File)
		}
		highest = max(highest, g)
	}
	return highest + 1, nil
}

// stagedEntry is the image's entry that boots next instead of the booted one: the highest
// generation any name gives, while its UKI is of the image and has tries left. false when that is
// the booted entry or none has tries.
func stagedEntry(entries []Entry, imageID string, booted Entry) (Entry, bool) {
	var highest Entry
	var generation uint64
	tied := false
	for _, e := range entries {
		g, ok, err := nameGeneration(e.ID, imageID)
		switch {
		case !ok:
		case err != nil:
			// A name past a uint64's range is higher than any other.
			return Entry{}, false
		case g > generation:
			highest, generation, tied = e, g, false
		case g == generation:
			tied = true
		}
	}
	// Of two names that give the highest generation, such as chalkos_06+3.efi and chalkos_6+3.efi,
	// systemd-boot's order does not tell which boots next.
	if generation == 0 || tied || highest.ImageID != imageID || highest.ID == booted.ID || highest.TriesLeft <= 0 {
		return Entry{}, false
	}
	return highest, true
}

// EntryName is the file name of the image's UKI of the generation without a tries counter, as an
// install writes it.
func EntryName(imageID string, generation uint64) string {
	return fmt.Sprintf("%s_%d.efi", imageID, generation)
}

// countedEntryName is the file name of the image's UKI of the generation with a tries counter, as
// an upgrade writes it.
func countedEntryName(imageID string, generation uint64, tries int) string {
	return fmt.Sprintf("%s_%d+%d.efi", imageID, generation, tries)
}

// parseEntryName splits a UKI's file name into the entry's ID and its tries counter as
// systemd-boot does: the counter follows the last "+" and precedes the suffix; a malformed one,
// or one whose counts exceed an int32, as systemd-boot rejects them, counts as none.
func parseEntryName(file string) (id string, left, done int) {
	const suffix = ".efi"
	plus := strings.LastIndexByte(file, '+')
	if plus < 0 {
		return strings.ToLower(file), -1, -1
	}
	counter := file[plus+1:]
	end := len(counter) - len(suffix)
	if end < 1 || !strings.EqualFold(counter[end:], suffix) {
		return strings.ToLower(file), -1, -1
	}
	l, d, hasDone := strings.Cut(counter[:end], "-")
	left, err := strconv.Atoi(l)
	if err != nil || left < 0 || left > math.MaxInt32 || strings.ContainsAny(l, "+-") {
		return strings.ToLower(file), -1, -1
	}
	done = 0
	if hasDone {
		if done, err = strconv.Atoi(d); err != nil || done < 0 || done > math.MaxInt32 || strings.ContainsAny(d, "+-") {
			return strings.ToLower(file), -1, -1
		}
	}
	return strings.ToLower(file[:plus] + suffix), left, done
}

// Entries lists the UKIs on the ESP mounted at esp, in name order, leaving out the
// files systemd-boot ignores.
func Entries(esp string) ([]Entry, error) {
	return entries(esp, os.Open)
}

// entries lists the UKIs, opening each with open. systemd-bless-boot renames the booted UKI once
// its boot was found healthy; a UKI gone between listing and reading is found under its new name
// by listing again.
func entries(esp string, open func(string) (*os.File, error)) ([]Entry, error) {
	list, err := listEntries(esp, open)
	if errors.Is(err, fs.ErrNotExist) {
		list, err = listEntries(esp, open)
	}
	return list, err
}

func listEntries(esp string, open func(string) (*os.File, error)) ([]Entry, error) {
	dir := filepath.Join(esp, linuxDir)
	files, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list the UKIs on the ESP: %w", err)
	}
	var entries []Entry
	for _, f := range files {
		name := f.Name()
		if f.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(strings.ToLower(name), ".efi") || strings.HasPrefix(strings.ToLower(name), "auto-") {
			continue
		}
		e := Entry{File: name}
		e.ID, e.TriesLeft, e.TriesDone = parseEntryName(name)
		img, readable, err := readUKI(filepath.Join(dir, name), open)
		if err != nil {
			return nil, fmt.Errorf("read %s on the ESP: %w", name, err)
		}
		if readable {
			e.Readable = true
			e.ImageID = img.ID()
			e.RootHash, _ = img.UsrHash()
			e.Generation = entryGeneration(e.ID, e.ImageID)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// readUKI reads the UKI at path; readable is false for a file that reads but does not parse as a
// UKI. A file that fails to open or read is an error instead: an upgrade removes the image's UKIs
// that do not parse, and one the ESP failed to give may boot the running store.
func readUKI(path string, open func(string) (*os.File, error)) (_ uki.Image, readable bool, _ error) {
	f, err := open(path)
	if err != nil {
		return uki.Image{}, false, err
	}
	defer f.Close()
	r := &recordingReader{r: f}
	img, err := uki.Read(r)
	if r.err != nil {
		return uki.Image{}, false, r.err
	}
	return img, err == nil, nil
}

// recordingReader keeps the first error a read gave other than the file's end: uki.Read reports
// it as a file that is no UKI.
type recordingReader struct {
	r   io.ReaderAt
	err error
}

func (r *recordingReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := r.r.ReadAt(p, off)
	if err != nil && err != io.EOF && r.err == nil {
		r.err = err
	}
	return n, err
}

// Booted finds the entry the node booted: the one systemd-boot says it selected, which must boot
// the running store; without that variable, as when firmware started the UKI itself, the one
// that boots the running store.
func Booted(entries []Entry, efivars string, running []byte) (Entry, error) {
	selected, err := loaderString(efivars, "LoaderEntrySelected")
	if err != nil {
		return Entry{}, err
	}
	for _, e := range entries {
		switch {
		case selected != "" && e.ID != strings.ToLower(selected):
		case !bytes.Equal(e.RootHash, running):
			if selected != "" {
				return Entry{}, fmt.Errorf("the boot loader entry %s does not boot the running store", selected)
			}
		default:
			return e, nil
		}
	}
	if selected != "" {
		return Entry{}, fmt.Errorf("the boot loader entry %s the node booted is not on the ESP", selected)
	}
	return Entry{}, errors.New("no UKI on the ESP boots the running store")
}

// EFI variable vendors: systemd-boot's, the global one and that of the signature databases.
const (
	loaderVendor   = "4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"
	globalVendor   = "8be4df61-93ca-11d2-aa0d-00e098032b8c"
	securityVendor = "d719b2cb-3d3a-4596-a3bc-dad00e67656f"
)

// readVariable reads an EFI variable's value from efivarfs; nil when it is not set.
func readVariable(efivars, name, vendor string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(efivars, name+"-"+vendor))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the EFI variable %s: %w", name, err)
	}
	// Four bytes of attributes precede the value.
	if len(data) < 4 {
		return nil, fmt.Errorf("the EFI variable %s is cut short", name)
	}
	return data[4:], nil
}

// loaderString reads a string variable of systemd-boot; "" when it is not set.
func loaderString(efivars, name string) (string, error) {
	data, err := readVariable(efivars, name, loaderVendor)
	if err != nil || data == nil {
		return "", err
	}
	if len(data)%2 != 0 {
		return "", fmt.Errorf("the EFI variable %s is not UTF-16", name)
	}
	units := make([]uint16, len(data)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(data[2*i:])
	}
	return strings.TrimRight(string(utf16.Decode(units)), "\x00"), nil
}

// syncESP flushes the ESP's file system: the boot loader reads it without the kernel's caches.
func syncESP(esp string) error {
	d, err := os.Open(filepath.Join(esp, linuxDir))
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync the ESP: %w", err)
	}
	if err := unix.Syncfs(int(d.Fd())); err != nil {
		return fmt.Errorf("sync the ESP: %w", err)
	}
	return nil
}

// RebootHelps says whether rebooting after a boot that was not found healthy leads systemd-boot
// on, and why: to another try while the booted entry has tries left, or on its last try to an
// entry it falls back to. Rebooting a boot the loader does not count, or one with nothing to fall
// back to, would boot the same image again, endlessly.
func RebootHelps(entries []Entry, efivars, cmdline string) (bool, string) {
	running, err := uki.UsrHash(cmdline)
	if err != nil {
		return false, fmt.Sprintf("the running store: %v", err)
	}
	booted, err := Booted(entries, efivars, running)
	if err != nil {
		return false, err.Error()
	}
	switch {
	case booted.TriesLeft < 0:
		return false, fmt.Sprintf("the boot loader does not count the boots of %s", booted.File)
	case booted.TriesLeft > 0:
		return true, fmt.Sprintf("%s has %d tries left", booted.File, booted.TriesLeft)
	}
	for _, e := range entries {
		if e.ID != booted.ID && !e.Bad() && e.RootHash != nil && !bytes.Equal(e.RootHash, running) {
			return true, fmt.Sprintf("%s used up its tries, and the boot loader falls back to %s", booted.File, e.File)
		}
	}
	return false, fmt.Sprintf("%s used up its tries, and there is no image to fall back to", booted.File)
}
