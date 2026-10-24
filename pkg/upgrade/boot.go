package upgrade

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"github.com/trevex/chalkos/pkg/esp"
	"github.com/trevex/chalkos/pkg/uki"
)

// Booted finds the entry the node booted: the one systemd-boot says it selected, which must boot
// the running store; without that variable, as when firmware started the UKI itself, the one
// that boots the running store.
func Booted(entries []esp.Entry, efivars string, running []byte) (esp.Entry, error) {
	selected, err := loaderString(efivars, "LoaderEntrySelected")
	if err != nil {
		return esp.Entry{}, err
	}
	for _, e := range entries {
		switch {
		case selected != "" && e.ID != strings.ToLower(selected):
		case !bytes.Equal(e.RootHash, running):
			if selected != "" {
				return esp.Entry{}, fmt.Errorf("the boot loader entry %s does not boot the running store", selected)
			}
		default:
			return e, nil
		}
	}
	if selected != "" {
		return esp.Entry{}, fmt.Errorf("the boot loader entry %s the node booted is not on the ESP", selected)
	}
	return esp.Entry{}, errors.New("no UKI on the ESP boots the running store")
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

// RebootHelps says whether rebooting after a boot that was not found healthy leads systemd-boot
// on, and why: to another try while the booted entry has tries left, or on its last try to an
// entry it falls back to. Rebooting a boot the loader does not count, or one with nothing to fall
// back to, would boot the same image again, endlessly.
func RebootHelps(entries []esp.Entry, efivars, cmdline string) (bool, string) {
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
