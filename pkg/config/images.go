package config

import (
	"fmt"

	"github.com/trevex/chalkos/pkg/imageref"
	"github.com/trevex/chalkos/pkg/schema"
)

// UnpinnedImages names each image reference of the documents that has a tag but no digest: every
// value whose schema has the format image-reference or versioned-image-reference. containerd
// pulls such an image by whatever the tag points to when it pulls, so two nodes may run different
// images under one reference. Each line names the document, the field and the reference.
func (docs Documents) UnpinnedImages(schemas *schema.Schemas) []string {
	var lines []string
	for _, d := range docs {
		walkDocument(d, schemas, func(n node) bool {
			format, _ := n.sch["format"].(string)
			if format != "image-reference" && format != "versioned-image-reference" {
				return true
			}
			s, ok := n.v.(string)
			if !ok {
				return true
			}
			if r, err := imageref.Parse(s); err == nil && !r.Pinned() {
				lines = append(lines, fmt.Sprintf("%s: %s: %s names a tag but no digest, so nodes pull whatever the tag points to when they pull; pin it as %s@sha256:<digest>", d, n.path, s, s))
			}
			return true
		})
	}
	return lines
}
