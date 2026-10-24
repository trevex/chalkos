package upgrade

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/trevex/chalkos/pkg/secureboot"
	"github.com/trevex/chalkos/pkg/storeslot"
	"github.com/trevex/chalkos/pkg/uki"
)

// Header describes an image as install and upgrade receive it: its store, the store's hash tree,
// its UKI and, in an install, its boot loader follow it in this order.
type Header struct {
	ImageID string
	// Platform is the platform the image is built for, such as metal or kvm.
	Platform string
	// Architecture is what the image runs on, as systemd names it: x86-64 or arm64.
	Architecture                   string
	RootHash                       []byte
	StoreSize, VeritySize, UKISize int64
	StoreSHA256, VeritySHA256      []byte
	UKISHA256                      []byte
	// BootLoaderSize and BootLoaderSHA256 describe the boot loader; zero and nil when the image
	// brings none.
	BootLoaderSize   int64
	BootLoaderSHA256 []byte
	// Provenance is what the build says of where it came from, which the node records and shows.
	Provenance Provenance
}

// Store is the image's store as a slot is written from it.
func (h Header) Store() storeslot.Image {
	return storeslot.Image{RootHash: h.RootHash, StoreSize: h.StoreSize, VeritySize: h.VeritySize, StoreSHA256: h.StoreSHA256, VeritySHA256: h.VeritySHA256}
}

// maxUKI bounds the UKI an image brings; it must fit the ESP beside another.
const maxUKI = 512 << 20

// maxBootLoader bounds the boot loader an image brings; systemd-boot takes about 100 KiB.
const maxBootLoader = 16 << 20

// Validate checks that the header describes an image whose parts can be received.
func (h Header) Validate() error {
	switch {
	case h.ImageID == "" || h.Platform == "":
		return errors.New("the image's ID and platform are required")
	case uki.BootLoaderName(h.Architecture) == "":
		return fmt.Errorf("the image's architecture %q is none chalkos builds images for", h.Architecture)
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

// CheckUKI checks the image's UKI against its header: it must name the image's ID and platform
// and an empty IMAGE_VERSION, boot the image's store and ask for a positive number of boot tries,
// and with Secure Boot enforced, firmware must accept its signature. It returns the tries.
func CheckUKI(r io.ReaderAt, h Header, efivars string) (int, error) {
	img, err := uki.Read(r)
	if err != nil {
		return 0, err
	}
	for _, field := range []struct{ name, uki, header string }{
		{"image ID", img.ID(), h.ImageID},
		{"platform", img.Platform(), h.Platform},
	} {
		if field.uki != field.header {
			return 0, fmt.Errorf("the UKI's %s is %q, but the image names %q", field.name, field.uki, field.header)
		}
	}
	if err := img.CheckImageVersion(); err != nil {
		return 0, err
	}
	if hash, err := img.UsrHash(); err != nil || !bytes.Equal(hash, h.RootHash) {
		return 0, errors.New("the UKI boots another store than the image carries")
	}
	if err := CheckArchitecture(r, h, "UKI"); err != nil {
		return 0, err
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
	db, dbx, enforced, err := secureboot.Databases(func(name, vendor string) ([]byte, error) {
		return readVariable(efivars, name, vendor)
	})
	if err != nil || !enforced {
		return err
	}
	if err := secureboot.VerifySignature(r, size, db, dbx); err != nil {
		return fmt.Errorf("Secure Boot would refuse the %s: %w", what, err)
	}
	return nil
}

// CheckArchitecture checks that an EFI binary of the image, the UKI or the boot loader, is built
// for the architecture the image names.
func CheckArchitecture(r io.ReaderAt, h Header, what string) error {
	arch, err := uki.Architecture(r)
	if err != nil {
		return fmt.Errorf("the %s: %w", what, err)
	}
	if arch != h.Architecture {
		return fmt.Errorf("the %s is built for %s, but the image names %s", what, arch, h.Architecture)
	}
	return nil
}
