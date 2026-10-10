package upgrade

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"

	"github.com/trevex/chalkos/pkg/uki"
)

// Header describes an image as install and upgrade receive it: its store, the store's hash tree,
// its UKI and, in an install, its boot loader follow it in this order.
type Header struct {
	ImageID, Version, Cluster, Role string
	RootHash                        []byte
	StoreSize, VeritySize, UKISize  int64
	StoreSHA256, VeritySHA256       []byte
	UKISHA256                       []byte
	// BootLoaderSize and BootLoaderSHA256 describe the boot loader; zero and nil when the image
	// brings none.
	BootLoaderSize   int64
	BootLoaderSHA256 []byte
}

// VersionPattern is what an image version may be: what a GPT label holds after "store-verity_"
// and what systemd-boot keeps unchanged in an entry's ID.
var VersionPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.~^-]{0,22}$`)

// maxUKI bounds the UKI an image brings; it must fit the ESP beside another.
const maxUKI = 512 << 20

// maxBootLoader bounds the boot loader an image brings; systemd-boot takes about 100 KiB.
const maxBootLoader = 16 << 20

// Validate checks that the header describes an image whose parts can be received.
func (h Header) Validate() error {
	switch {
	case !VersionPattern.MatchString(h.Version):
		return fmt.Errorf("the version %q is not 1 to 23 characters of a-z, 0-9, '.', '~', '^' and '-'", h.Version)
	case h.ImageID == "" || h.Cluster == "" || h.Role == "":
		return errors.New("the image's ID, cluster and role are required")
	case len(h.RootHash) != sha256.Size:
		return errors.New("the root hash is not a SHA-256")
	case len(h.StoreSHA256) != sha256.Size || len(h.VeritySHA256) != sha256.Size || len(h.UKISHA256) != sha256.Size:
		return errors.New("the SHA-256 sums of the store, the hash tree and the UKI are required")
	case h.StoreSize <= 0 || h.VeritySize <= 0 || h.UKISize <= 0 || h.UKISize > maxUKI:
		return errors.New("the sizes of the store, the hash tree and the UKI are required")
	case h.BootLoaderSize < 0 || h.BootLoaderSize > maxBootLoader:
		return fmt.Errorf("the boot loader of %d bytes is larger than %d", h.BootLoaderSize, maxBootLoader)
	case h.BootLoaderSize > 0 && len(h.BootLoaderSHA256) != sha256.Size, h.BootLoaderSize == 0 && h.BootLoaderSHA256 != nil:
		return errors.New("the boot loader's size and SHA-256 are required together")
	}
	return nil
}

// ReceiveUKI writes the UKI that follows in the stream to path, where systemd-boot must not find
// it, and checks it with CheckUKI. It returns the boot tries the image asks for.
func ReceiveUKI(path string, h Header, stream io.Reader, efivars string) (int, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, fmt.Errorf("write the UKI to the ESP: %w", err)
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(f, sum), stream, h.UKISize); err != nil {
		return 0, fmt.Errorf("receive the UKI: %w", err)
	}
	if err := f.Sync(); err != nil {
		return 0, fmt.Errorf("write the UKI to the ESP: %w", err)
	}
	if got := sum.Sum(nil); !bytes.Equal(got, h.UKISHA256) {
		return 0, fmt.Errorf("the UKI's SHA-256 is %x, want %x", got, h.UKISHA256)
	}
	tries, err := CheckUKI(f, h, efivars)
	if err != nil {
		return 0, err
	}
	return tries, f.Close()
}

// CheckUKI checks the image's UKI against its header: it must name the image's ID, version,
// cluster and role, boot the image's store and ask for a positive number of boot tries, and with
// Secure Boot enforced, firmware must accept its signature. It returns the tries.
func CheckUKI(r io.ReaderAt, h Header, efivars string) (int, error) {
	img, err := uki.Read(r)
	if err != nil {
		return 0, err
	}
	for _, field := range []struct{ name, uki, header string }{
		{"image ID", img.ID(), h.ImageID},
		{"version", img.Version(), h.Version},
		{"cluster", img.Cluster(), h.Cluster},
		{"role", img.Role(), h.Role},
	} {
		if field.uki != field.header {
			return 0, fmt.Errorf("the UKI's %s is %q, but the image names %q", field.name, field.uki, field.header)
		}
	}
	if hash, err := img.UsrHash(); err != nil || !bytes.Equal(hash, h.RootHash) {
		return 0, errors.New("the UKI boots another store than the image carries")
	}
	tries, err := strconv.Atoi(img.BootTries())
	if err != nil || tries < 1 {
		return 0, fmt.Errorf("the UKI's boot tries %q are not a positive number", img.BootTries())
	}
	if err := CheckSecureBoot(r, h.UKISize, efivars, "UKI"); err != nil {
		return 0, err
	}
	return tries, nil
}

// CheckSecureBoot checks the signature of an EFI binary of the image, the UKI or the boot
// loader, against the firmware's db and dbx, as firmware will when it starts the binary. It
// checks nothing when Secure Boot is not enforced.
func CheckSecureBoot(r io.ReaderAt, size int64, efivars, what string) error {
	db, dbx, enforced, err := secureBootDatabases(efivars)
	if err != nil || !enforced {
		return err
	}
	if err := uki.VerifySignature(r, size, db, dbx); err != nil {
		return fmt.Errorf("Secure Boot would refuse the %s: %w", what, err)
	}
	return nil
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
