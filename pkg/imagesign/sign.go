// Package imagesign signs the boot loader and UKIs of chalkos disk images for Secure Boot, in
// place or in a signed copy of an image directory.
package imagesign

import (
	"errors"
	"fmt"
	"os"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/fatfs"
	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/secureboot"
)

// SignImage signs the boot loader (/EFI/BOOT/BOOT*.EFI) and the UKIs (/EFI/Linux/*.efi) on the
// image's ESP in place with the db key in the PEM file key and its certificate in cert. Other EFI
// binaries stay unsigned, so whatever else lands on the ESP does not gain the db signature. The
// UKI's signature covers the store through the verity root hash on its command line.
func SignImage(raw string, espOffset int64, key, cert string) error {
	signer, err := secureboot.LoadSigner(key, cert)
	if err != nil {
		return err
	}
	return signImage(raw, espOffset, signer)
}

// signImage is SignImage with the signer read.
func signImage(raw string, espOffset int64, signer secureboot.KeyPair) (err error) {
	esp, err := fatfs.Open(raw, espOffset, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, esp.Close()) }()
	// Only direct children match.
	files, err := image.ListESP(esp, "/EFI/BOOT/BOOT*.EFI", "/EFI/LINUX/*.EFI")
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no boot loader or UKI on the ESP of %s", raw)
	}
	for _, f := range files {
		data, err := esp.ReadFile(f)
		if err != nil {
			return err
		}
		if data, err = secureboot.Sign(data, signer); err != nil {
			return fmt.Errorf("sign %s: %w", f, err)
		}
		if err := esp.WriteFile(f, data); err != nil {
			return err
		}
	}
	return nil
}

// signFile signs an EFI binary in place.
func signFile(file string, signer secureboot.KeyPair) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if data, err = secureboot.Sign(data, signer); err != nil {
		return fmt.Errorf("sign %s: %w", file, err)
	}
	return atomicfile.Replace(file, data, 0o644)
}
