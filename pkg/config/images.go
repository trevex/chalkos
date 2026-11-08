package config

import (
	"fmt"
	"strings"

	"github.com/trevex/chalkos/pkg/imageref"
	"github.com/trevex/chalkos/pkg/schema"
)

// UnpinnedImages names each image reference of the documents that has a tag but no digest: every
// value whose schema has the format image-reference or versioned-image-reference. containerd
// pulls such an image by whatever the tag points to when it pulls, so two nodes may run different
// images under one reference. Each line names the document, the field below spec and the reference.
func (docs Documents) UnpinnedImages(schemas *schema.Schemas) []string {
	var lines []string
	for _, d := range docs {
		walk(d, schemas, func(n schema.Node) bool {
			if f := n.Schema.Format; f == nil || f.Name != "image-reference" && f.Name != "versioned-image-reference" {
				return true
			}
			s, ok := n.Value.(string)
			if !ok {
				return true
			}
			if r, err := imageref.Parse(s); err == nil && !r.Pinned() {
				field := strings.TrimPrefix(n.Path, "spec.")
				lines = append(lines, fmt.Sprintf("%s: %s: %s names a tag but no digest, so nodes pull whatever the tag points to when they pull; pin it as %s@sha256:<digest>", d, field, s, s))
			}
			return true
		})
	}
	return lines
}
