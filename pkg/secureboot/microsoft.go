package secureboot

import (
	"crypto/x509"
	"embed"
	"fmt"
)

// Microsoft's Secure Boot certificates, which firmware ships enrolled and which sign option ROMs,
// shims and other boot loaders. They come from python3Packages.virt-firmware 25.12 in nixpkgs,
// virt/firmware/certs/microsoft.com, converted to DER. Their SHA-256 fingerprints:
//
//	ms-kek-2011.der       Microsoft Corporation KEK CA 2011
//	                      a1117f516a32cefcba3f2d1ace10a87972fd6bbe8fe0d0b996e09e65d802a503
//	ms-kek-2023.der       Microsoft Corporation KEK 2K CA 2023
//	                      3cd3f0309edae228767a976dd40d9f4affc4fbd5218f2e8cc3c9dd97e8ac6f9d
//	ms-uefi-2011.der      Microsoft Corporation UEFI CA 2011
//	                      48e99b991f57fc52f76149599bff0a58c47154229b9f8d603ac40d3500248507
//	ms-uefi-2023.der      Microsoft UEFI CA 2023
//	                      f6124e34125bee3fe6d79a574eaa7b91c0e7bd9d929c1a321178efd611dad901
//	ms-uefi-rom-2023.der  Microsoft Option ROM UEFI CA 2023
//	                      e5be3e64c6e66a281457ecdece0d6d0787577aad2a3a0144262c10c14ba8d8f1
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
