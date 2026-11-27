package sdk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/google/go-containerregistry/pkg/v1/types"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/ociimage"
)

// RegistryImageHeader is the header of the image in a registry a, as a node that pulls it is sent
// it: the image's ID, platform, architecture, root hash and provenance, and the size and SHA-256
// of its store, hash tree and UKI, and of its boot loader when bootLoader is set, from its
// layers' descriptors. An install onto a disk writes the boot loader; an upgrade and an install
// in place keep the node's.
func RegistryImageHeader(a *ociimage.Artifact, bootLoader bool) (*nodev1.ImageHeader, error) {
	root, err := hex.DecodeString(a.Header.RootHash)
	if err != nil {
		return nil, fmt.Errorf("%s: the root hash: %w", a.Reference, err)
	}
	h := &nodev1.ImageHeader{
		ImageId: a.Header.ImageID, Platform: a.Header.Platform, Architecture: a.Header.Architecture,
		RootHash: root, Revision: a.Header.Revision, Tag: a.Header.Tag,
	}
	type layer struct {
		mediaType types.MediaType
		part      **nodev1.ImagePart
	}
	layers := []layer{{ociimage.StoreType, &h.Store}, {ociimage.HashTreeType, &h.HashTree}, {ociimage.UKIType, &h.Uki}}
	if bootLoader {
		layers = append(layers, layer{ociimage.BootLoaderType, &h.BootLoader})
	}
	for _, p := range layers {
		d, err := a.Descriptor(p.mediaType)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.Reference, err)
		}
		sum, err := hex.DecodeString(d.Digest.Hex)
		if d.Digest.Algorithm != "sha256" || err != nil || len(sum) != sha256.Size {
			return nil, fmt.Errorf("%s: the %s layer's digest %s is no SHA-256", a.Reference, p.mediaType, d.Digest)
		}
		*p.part = &nodev1.ImagePart{Size: uint64(d.Size), Sha256: sum}
	}
	return h, nil
}
