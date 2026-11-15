package secureboot

import (
	"crypto/x509"
	"embed"
	"fmt"
)

// Microsoft's Secure Boot certificates, which firmware ships enrolled and which sign option ROMs,
// shims and other boot loaders. They come from python3Packages.virt-firmware 25.12 in nixpkgs,
// virt/firmware/certs/microsoft.com, converted to DER; TestMicrosoftCertificates pins their
// fingerprints.
//
//go:embed microsoft/*.der
var microsoftCerts embed.FS

// MicrosoftOwner is the GUID that owns Microsoft's entries in the signature lists firmware ships,
// 77fa9abd-0359-4d32-bd60-28f4e78f784b, as it is stored.
var MicrosoftOwner = guid(0x77fa9abd, 0x0359, 0x4d32, [8]byte{0xbd, 0x60, 0x28, 0xf4, 0xe7, 0x8f, 0x78, 0x4b})

// MicrosoftKEK returns Microsoft's KEK CAs of 2011 and 2023, with which Microsoft signs updates of
// db and dbx.
func MicrosoftKEK() []*x509.Certificate {
	return microsoft("ms-kek-2011.der", "ms-kek-2023.der")
}

// MicrosoftDB returns Microsoft's UEFI CAs of 2011 and 2023, which sign shims and other boot
// loaders, and its Option ROM UEFI CA of 2023, which signs the option ROMs of graphics and network
// cards; the CA of 2011 signs both.
func MicrosoftDB() []*x509.Certificate {
	return microsoft("ms-uefi-2011.der", "ms-uefi-2023.der", "ms-uefi-rom-2023.der")
}

func microsoft(files ...string) []*x509.Certificate {
	var certs []*x509.Certificate
	for _, f := range files {
		der, err := microsoftCerts.ReadFile("microsoft/" + f)
		if err != nil {
			panic(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			panic(fmt.Sprintf("the embedded %s: %v", f, err))
		}
		certs = append(certs, cert)
	}
	return certs
}

// MicrosoftOptionROMCAs returns the CAs of MicrosoftDB that sign option ROMs: the UEFI CA of 2011
// and the Option ROM UEFI CA of 2023. The UEFI CA of 2023 signs boot loaders alone.
func MicrosoftOptionROMCAs() []*x509.Certificate {
	return microsoft("ms-uefi-2011.der", "ms-uefi-rom-2023.der")
}
