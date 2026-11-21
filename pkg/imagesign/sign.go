// Package imagesign signs the boot loader and UKIs of chalkos disk images for Secure Boot, in
// place or in a signed copy of an image directory, and with each UKI the module policy it carries.
package imagesign

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/fatfs"
	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/modulepolicy"
	"github.com/trevex/chalkos/pkg/secureboot"
	"github.com/trevex/chalkos/pkg/uki"
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
		sign := secureboot.Sign
		if strings.HasPrefix(strings.ToUpper(f), "/EFI/LINUX/") {
			sign = signUKI
		}
		if data, err = sign(data, signer); err != nil {
			return fmt.Errorf("sign %s: %w", f, err)
		}
		if err := esp.WriteFile(f, data); err != nil {
			return err
		}
	}
	return nil
}

// signFile signs an EFI binary in place with sign, secureboot.Sign or signUKI.
func signFile(file string, signer secureboot.KeyPair, sign func([]byte, secureboot.KeyPair) ([]byte, error)) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if data, err = sign(data, signer); err != nil {
		return fmt.Errorf("sign %s: %w", file, err)
	}
	return atomicfile.Replace(file, data, 0o644)
}

// signUKI signs a UKI for Secure Boot: first the module policy its first initrd carries, in
// place, with the db key as PKCS #7, as the kernel loads it from the platform keyring; then the
// UKI itself. A UKI without a policy, of an image built without one, is signed as it is. The
// policy must allow the store the UKI boots and no other.
func signUKI(data []byte, signer secureboot.KeyPair) ([]byte, error) {
	off, size, ok, err := uki.PolicySlot(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("the UKI's module policy: %w", err)
	}
	if ok {
		if data, err = signPolicy(data, off, size, signer); err != nil {
			return nil, err
		}
	}
	return secureboot.Sign(data, signer)
}

// signPolicy replaces the module policy at off in a copy of the UKI with its signature. A policy
// signed before is signed again from the text its signature carries.
func signPolicy(data []byte, off, size int64, signer secureboot.KeyPair) ([]byte, error) {
	signed, text, err := modulepolicy.Trim(data[off : off+size])
	if err != nil {
		return nil, err
	}
	if signed {
		if text, err = secureboot.AttachedContent(text); err != nil {
			return nil, fmt.Errorf("the UKI's signed module policy: %w", err)
		}
	}
	img, err := uki.Read(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	boots, err := img.UsrHash()
	if err != nil {
		return nil, err
	}
	allows, err := modulepolicy.RootHash(text)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(allows, boots) {
		return nil, fmt.Errorf("the module policy allows the store %x, but the UKI boots %x", allows, boots)
	}
	der, err := secureboot.SignAttached(text, signer)
	if err != nil {
		return nil, err
	}
	if int64(len(der)) > size {
		return nil, fmt.Errorf("the signed module policy takes %d bytes, more than the %d its file holds; sign with a shorter certificate", len(der), size)
	}
	out := bytes.Clone(data)
	copy(out[off:], der)
	clear(out[off+int64(len(der)) : off+size])
	return out, nil
}
