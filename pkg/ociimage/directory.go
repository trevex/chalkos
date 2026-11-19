package ociimage

import (
	"context"
	"encoding/hex"
	"fmt"
	"path/filepath"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/schema"
)

// FromDirectory is the chalkos image of an image directory as nix build makes it. It refuses
// metadata.json of another image and an image without a name, such as the installer.
// closeImage removes the UKI and boot loader copies the image reads, so write the image first.
func FromDirectory(ctx context.Context, dir string) (_ v1.Image, closeImage func() error, err error) {
	raw, err := image.FindRaw(dir)
	if err != nil {
		return nil, nil, err
	}
	c, err := ReadContractDir(dir)
	if err != nil {
		return nil, nil, err
	}
	md, err := schema.ParseMetadata(c.Metadata)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", dir, err)
	}
	partitions, err := image.ReadPartitions(filepath.Join(dir, "repart-output.json"))
	if err != nil {
		return nil, nil, err
	}
	p, err := image.OpenParts(ctx, raw, partitions, true)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			p.Close()
		}
	}()
	if p.Info.Name() == "" {
		return nil, nil, fmt.Errorf("%s names no image name in its UKI's os-release; it is no node image", dir)
	}
	h := Header{
		ImageID:      p.Info.ID(),
		RootHash:     hex.EncodeToString(p.Store.RootHash),
		Architecture: p.Architecture,
		Platform:     p.Info.Platform(),
		Revision:     md.Revision,
		Tag:          md.Tag,
	}
	if err := describes(md, h); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", dir, err)
	}
	store, err := SectionBlob(StoreType, p.Store.Data)
	if err != nil {
		return nil, nil, err
	}
	hashTree, err := SectionBlob(HashTreeType, p.Store.HashTree)
	if err != nil {
		return nil, nil, err
	}
	uki, err := FileBlob(UKIType, p.UKI)
	if err != nil {
		return nil, nil, err
	}
	bootLoader, err := FileBlob(BootLoaderType, p.BootLoader)
	if err != nil {
		return nil, nil, err
	}
	contract, err := c.Tar()
	if err != nil {
		return nil, nil, err
	}
	img, err := Build(h, md.Annotations, store, hashTree, uki, bootLoader, BytesBlob(ContractType, contract))
	if err != nil {
		return nil, nil, err
	}
	return img, p.Close, nil
}

// describes refuses metadata.json of another image than the header's: of another root hash or
// another platform.
func describes(md schema.Metadata, h Header) error {
	switch {
	case md.RootHash != h.RootHash:
		return fmt.Errorf("metadata.json names the root hash %q, but the image's store has %s", md.RootHash, h.RootHash)
	case md.Platform != h.Platform:
		return fmt.Errorf("metadata.json names the platform %q, but the image's UKI names %q", md.Platform, h.Platform)
	}
	return nil
}
