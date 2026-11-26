package sdk

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/schema"
)

// CheckImageOSCA refuses an image whose metadata names an OS CA that none of the secrets' OS CAs
// is: neither the one that issues nor, during a rotation, one accepted besides it. A node that
// boots such an image in maintenance mode refuses every client certificate the secrets issue, so
// the error wraps ErrOSCARefused. An image without an OS CA accepts any client in maintenance mode
// and passes.
func CheckImageOSCA(md schema.Metadata, s clustersecrets.Secrets) error {
	if md.OSCA == "" {
		return nil
	}
	image, err := pki.ParseBundle(md.OSCA)
	if err != nil {
		return fmt.Errorf("the image's OS CA: %w", err)
	}
	trusted, err := pki.ParseBundle(s.OSCABundle())
	if err != nil {
		return fmt.Errorf("the secrets' OS CAs: %w", err)
	}
	for _, c := range image {
		for _, t := range trusted {
			if bytes.Equal(c.Raw, t.Raw) {
				return nil
			}
		}
	}
	imageFPs, _ := pki.Fingerprints(md.OSCA)
	trustedFPs, _ := pki.Fingerprints(s.OSCABundle())
	return fmt.Errorf("%w: the image carries the OS CA %s, the secrets trust %s",
		ErrOSCARefused, strings.Join(imageFPs, ", "), strings.Join(trustedFPs, ", "))
}
