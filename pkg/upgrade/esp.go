package upgrade

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
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
	// Version is the image version its os-release names, and RootHash the root hash of the store
	// it boots; both are empty when the UKI cannot be read.
	Version  string
	RootHash []byte
}

// Bad reports whether systemd-boot used up the entry's tries.
func (e Entry) Bad() bool { return e.TriesLeft == 0 }

// parseEntryName splits a UKI's file name into the entry's ID and its tries counter as
// systemd-boot does: the counter follows the last "+" and precedes the suffix; a malformed one
// counts as none.
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
	if err != nil || left < 0 || strings.ContainsAny(l, "+-") {
		return strings.ToLower(file), -1, -1
	}
	done = 0
	if hasDone {
		if done, err = strconv.Atoi(d); err != nil || done < 0 || strings.ContainsAny(d, "+-") {
			return strings.ToLower(file), -1, -1
		}
	}
	return strings.ToLower(file[:plus] + suffix), left, done
}

// entryName is a UKI's file name with a tries counter, as an upgrade installs it.
func entryName(id, version string, tries int) string {
	return fmt.Sprintf("%s_%s+%d.efi", id, version, tries)
}

// Entries lists the UKIs on the ESP mounted at esp, in name order, leaving out the
// files systemd-boot ignores.
func Entries(esp string) ([]Entry, error) {
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
		if img, err := readUKI(filepath.Join(dir, name)); err == nil {
			e.Version = img.Version()
			e.RootHash, _ = img.UsrHash()
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func readUKI(path string) (uki.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return uki.Image{}, err
	}
	defer f.Close()
	return uki.Read(f)
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

// fsImmutable is FS_IMMUTABLE_FL, the inode flag efivarfs sets on variables.
const fsImmutable = 0x10

// setLoaderString sets a non-volatile string variable of systemd-boot. efivarfs makes a
// variable's file immutable and takes a new value in one write, so the old one is cleared and
// removed first.
func setLoaderString(efivars, name, value string) error {
	path := filepath.Join(efivars, name+"-"+loaderVendor)
	if f, err := os.Open(path); err == nil {
		// A file system without the flag, such as a test's, has nothing to clear.
		if flags, err := unix.IoctlGetUint32(int(f.Fd()), unix.FS_IOC_GETFLAGS); err == nil && flags&fsImmutable != 0 {
			if err := unix.IoctlSetPointerInt(int(f.Fd()), unix.FS_IOC_SETFLAGS, int(flags&^fsImmutable)); err != nil {
				f.Close()
				return fmt.Errorf("make the EFI variable %s writable: %w", name, err)
			}
		}
		f.Close()
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove the EFI variable %s: %w", name, err)
	}
	// Non-volatile, and available to the boot loader and the OS.
	data := binary.LittleEndian.AppendUint32(nil, 0x7)
	for _, u := range utf16.Encode([]rune(value + "\x00")) {
		data = binary.LittleEndian.AppendUint16(data, u)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("set the EFI variable %s: %w", name, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("set the EFI variable %s: %w", name, err)
	}
	return f.Close()
}

// secureBootDatabases returns db and dbx when Secure Boot is enforced, and ok false when it is
// not: disabled, or in setup mode.
func secureBootDatabases(efivars string) (db, dbx uki.Database, ok bool, err error) {
	enabled, err := readVariable(efivars, "SecureBoot", globalVendor)
	if err != nil {
		return uki.Database{}, uki.Database{}, false, err
	}
	setup, err := readVariable(efivars, "SetupMode", globalVendor)
	if err != nil {
		return uki.Database{}, uki.Database{}, false, err
	}
	if len(enabled) != 1 || enabled[0] != 1 || len(setup) == 1 && setup[0] == 1 {
		return uki.Database{}, uki.Database{}, false, nil
	}
	for _, v := range []struct {
		name string
		into *uki.Database
	}{{"db", &db}, {"dbx", &dbx}} {
		data, err := readVariable(efivars, v.name, securityVendor)
		if err != nil {
			return uki.Database{}, uki.Database{}, false, err
		}
		if *v.into, err = uki.ParseDatabase(data); err != nil {
			return uki.Database{}, uki.Database{}, false, fmt.Errorf("the firmware's %s: %w", v.name, err)
		}
	}
	return db, dbx, true, nil
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
