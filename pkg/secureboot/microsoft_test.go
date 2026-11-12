package secureboot

import (
	"crypto/rsa"
	"crypto/x509"
	"slices"
	"testing"

	"github.com/trevex/chalkos/pkg/pki"
)

// TestMicrosoftCertificates holds the embedded certificates to the subjects and fingerprints
// microsoft.go names.
func TestMicrosoftCertificates(t *testing.T) {
	type want struct{ name, fingerprint string }
	for _, tc := range []struct {
		what  string
		certs []*x509.Certificate
		want  []want
	}{
		{"KEK", MicrosoftKEK(), []want{
			{"Microsoft Corporation KEK CA 2011", "a1117f516a32cefcba3f2d1ace10a87972fd6bbe8fe0d0b996e09e65d802a503"},
			{"Microsoft Corporation KEK 2K CA 2023", "3cd3f0309edae228767a976dd40d9f4affc4fbd5218f2e8cc3c9dd97e8ac6f9d"},
		}},
		{"db", MicrosoftDB(), []want{
			{"Microsoft Corporation UEFI CA 2011", "48e99b991f57fc52f76149599bff0a58c47154229b9f8d603ac40d3500248507"},
			{"Microsoft UEFI CA 2023", "f6124e34125bee3fe6d79a574eaa7b91c0e7bd9d929c1a321178efd611dad901"},
			{"Microsoft Option ROM UEFI CA 2023", "e5be3e64c6e66a281457ecdece0d6d0787577aad2a3a0144262c10c14ba8d8f1"},
		}},
	} {
		if len(tc.certs) != len(tc.want) {
			t.Fatalf("%s: %d certificates, want %d", tc.what, len(tc.certs), len(tc.want))
		}
		for i, c := range tc.certs {
			if c.Subject.CommonName != tc.want[i].name || pki.Fingerprint(c.Raw) != tc.want[i].fingerprint {
				t.Errorf("%s: certificate %d is %q, %s; want %+v", tc.what, i, c.Subject.CommonName, pki.Fingerprint(c.Raw), tc.want[i])
			}
			// Firmware takes RSA keys alone into KEK and db.
			if _, ok := c.PublicKey.(*rsa.PublicKey); !ok {
				t.Errorf("%s: %q has a key other than RSA", tc.what, c.Subject.CommonName)
			}
		}
	}
	if GUIDString(MicrosoftOwner) != "77fa9abd-0359-4d32-bd60-28f4e78f784b" {
		t.Errorf("MicrosoftOwner = %s", GUIDString(MicrosoftOwner))
	}
}

// TestMicrosoftOptionROMCAs are the UEFI CA of 2011 and the Option ROM UEFI CA of 2023.
func TestMicrosoftOptionROMCAs(t *testing.T) {
	var names []string
	for _, c := range MicrosoftOptionROMCAs() {
		names = append(names, c.Subject.CommonName)
	}
	if want := []string{"Microsoft Corporation UEFI CA 2011", "Microsoft Option ROM UEFI CA 2023"}; !slices.Equal(names, want) {
		t.Errorf("MicrosoftOptionROMCAs = %v, want %v", names, want)
	}
}
