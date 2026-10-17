package chalkctl

import (
	"encoding/hex"
	"strings"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
)

// shortHash is how many hex digits of a root hash, or of a git revision, name an image.
const shortHash = 12

// imageName names an image by its store's root hash, with the tag and revision it was built from
// when they are known, such as 3fa9c1e2b7d0 (v1.5.0, a1b2c3d4e5f6). The root hash identifies the
// image; the tag and revision only say where it came from.
func imageName(rootHash []byte, revision, tag string) string {
	name := hex.EncodeToString(rootHash)
	name = name[:min(len(name), shortHash)]
	if name == "" {
		name = "unknown"
	}
	var about []string
	if tag != "" {
		about = append(about, tag)
	}
	if revision != "" {
		// A flake's dirty revision ends in -dirty, which says more than the digits it cuts.
		r, dirty := strings.CutSuffix(revision, "-dirty")
		r = r[:min(len(r), shortHash)]
		if dirty {
			r += "-dirty"
		}
		about = append(about, r)
	}
	if len(about) > 0 {
		name += " (" + strings.Join(about, ", ") + ")"
	}
	return name
}

// bootImageName names an image a node reports.
func bootImageName(b *nodev1.BootImage) string {
	return imageName(b.GetRootHash(), b.GetRevision(), b.GetTag())
}

// name names the image chalkctl sends.
func (img *diskImage) name() string {
	return imageName(img.header.GetRootHash(), img.header.GetRevision(), img.header.GetTag())
}
