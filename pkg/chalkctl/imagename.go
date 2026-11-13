package chalkctl

import "github.com/trevex/chalkos/pkg/statusview"

// imageName names an image by its store's root hash, with the tag and revision it was built from
// when they are known, as statusview.ImageName does.
func imageName(rootHash []byte, revision, tag string) string {
	return statusview.ImageName(rootHash, revision, tag)
}

// name names the image chalkctl sends.
func (img *diskImage) name() string {
	return imageName(img.header.GetRootHash(), img.header.GetRevision(), img.header.GetTag())
}
