package sdk_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/sdk"
)

// TestCheckImageOSCA passes an image without an OS CA and one that carries the OS CA the secrets
// issue with, and refuses an image of another OS CA, also one the secrets accept besides the
// issuing one during a rotation, as the image in maintenance mode trusts its own OS CA alone.
func TestCheckImageOSCA(t *testing.T) {
	s := generateSecrets(t)
	other := generateSecrets(t)
	if err := sdk.CheckImageOSCA(schema.Metadata{}, s); err != nil {
		t.Errorf("an image without an OS CA: %v", err)
	}
	if err := sdk.CheckImageOSCA(schema.Metadata{OSCA: s.OSCA.Certificate}, s); err != nil {
		t.Errorf("the secrets' OS CA: %v", err)
	}
	err := sdk.CheckImageOSCA(schema.Metadata{OSCA: other.OSCA.Certificate}, s)
	if !errors.Is(err, sdk.ErrOSCARefused) {
		t.Fatalf("another OS CA: %v, want ErrOSCARefused", err)
	}
	fp, _ := pki.Fingerprints(other.OSCA.Certificate)
	if want := fp[0]; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q, want it to name the image's OS CA %s", err, want)
	}
	rotating := s
	rotating.Accepted = clustersecrets.Accepted{OSCA: []string{other.OSCA.Certificate}}
	if err := sdk.CheckImageOSCA(schema.Metadata{OSCA: other.OSCA.Certificate}, rotating); !errors.Is(err, sdk.ErrOSCARefused) {
		t.Errorf("an OS CA accepted besides the one that issues: %v, want ErrOSCARefused", err)
	}
	if err := sdk.CheckImageOSCA(schema.Metadata{OSCA: rotating.OSCABundle()}, rotating); err != nil {
		t.Errorf("a bundle that holds the issuing OS CA: %v", err)
	}
	if err := sdk.CheckImageOSCA(schema.Metadata{OSCA: "not PEM"}, s); err == nil || errors.Is(err, sdk.ErrOSCARefused) {
		t.Errorf("an OS CA that is no certificate: %v, want an error other than ErrOSCARefused", err)
	}
}
