package sdk

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/schema"
)

// CheckImageOSCA refuses an image whose metadata names an OS CA that is not the one that issues
// the secrets' client certificates. A node that boots the image in maintenance mode trusts the
// image's OS CA alone, also after an install that failed after activating it, so it refuses every
// client certificate the secrets issue, and the error wraps ErrOSCARefused. An OS CA accepted
// besides the issuing one during a rotation does not do: an image built before a rotation of the
// OS CA finished must be built again with the current one. An image without an OS CA accepts any
// client in maintenance mode and passes.
func CheckImageOSCA(md schema.Metadata, s clustersecrets.Secrets) error {
	if md.OSCA == "" {
		return nil
	}
	image, err := pki.ParseBundle(md.OSCA)
	if err != nil {
		return fmt.Errorf("the image's OS CA: %w", err)
	}
	issuing, err := pki.ParseBundle(s.OSCA.Certificate)
	if err != nil {
		return fmt.Errorf("the secrets' OS CA: %w", err)
	}
	for _, c := range image {
		for _, t := range issuing {
			if bytes.Equal(c.Raw, t.Raw) {
				return nil
			}
		}
	}
	imageFPs, _ := pki.Fingerprints(md.OSCA)
	issuingFPs, _ := pki.Fingerprints(s.OSCA.Certificate)
	return fmt.Errorf("%w: the image carries the OS CA %s, the secrets issue client certificates with %s; build the image again with the current OS CA",
		ErrOSCARefused, strings.Join(imageFPs, ", "), strings.Join(issuingFPs, ", "))
}
