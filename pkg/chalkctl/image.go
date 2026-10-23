package chalkctl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/v1/types"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/inventory"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// diskImage is what install and upgrade send of a disk image: its parts, the UKI and the boot
// loader, and the header that describes them to the nodes.
type diskImage struct {
	*image.Parts
	// header describes the image to the nodes.
	header *nodev1.ImageHeader
	// contract is what an upgrade's image directory says the image accepts as config, which
	// chalkctl checks the inventory's config against; nil for an install's image from a directory.
	contract *contract
	// definitions is the directory of the image's repart definitions of its system region.
	definitions string
	// source is where the node takes the image's parts from: nil when chalkctl streams them,
	// an image in a registry when the node pulls them.
	source *nodev1.ImageSource
	// path is the image directory or raw image the image was read from; empty for an image in a
	// registry.
	path string
}

// openImage reads an image for an install, with its boot loader, or an upgrade: a raw image with
// repart-output.json next to it, or the directory nix build makes. Warnings go to stderr.
func openImage(ctx context.Context, path string, bootLoader bool, stderr io.Writer) (_ *diskImage, err error) {
	raw, definitions, err := imageFiles(path)
	if err != nil {
		return nil, err
	}
	parts, err := image.ReadPartitions(filepath.Join(filepath.Dir(raw), "repart-output.json"))
	if err != nil {
		return nil, err
	}
	p, err := image.OpenParts(ctx, raw, parts, bootLoader)
	if err != nil {
		return nil, err
	}
	img := &diskImage{Parts: p, definitions: definitions, path: path}
	defer func() {
		if err != nil {
			img.Close()
		}
	}()
	// The node would refuse the UKI only once it received the image.
	if err := upgrade.CheckImageVersion(img.Info); err != nil {
		return nil, fmt.Errorf("the image's UKI: %w", err)
	}
	if img.Info.Variant() == "" || img.Info.Platform() == "" {
		return nil, errors.New("the image names no variant or platform in its os-release; it is no node image")
	}
	h := &nodev1.ImageHeader{
		ImageId:      img.Info.ID(),
		Platform:     img.Info.Platform(),
		RootHash:     img.Store.RootHash,
		Architecture: img.Architecture,
	}
	h.Revision, h.Tag = provenance(filepath.Dir(raw), img.Store.RootHash, stderr)
	if h.Store, err = sum(img.Store.Data); err != nil {
		return nil, err
	}
	if h.HashTree, err = sum(img.Store.HashTree); err != nil {
		return nil, err
	}
	if h.Uki, err = sumFile(img.UKI); err != nil {
		return nil, err
	}
	if bootLoader {
		if h.BootLoader, err = sumFile(img.BootLoader); err != nil {
			return nil, err
		}
	}
	img.header = h
	return img, nil
}

func sum(r io.Reader) (*nodev1.ImagePart, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return nil, err
	}
	return &nodev1.ImagePart{Size: uint64(n), Sha256: h.Sum(nil)}, nil
}

// sumFile is the size and SHA-256 of a file.
func sumFile(path string) (*nodev1.ImagePart, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return sum(f)
}

// openUpgradeImage opens an upgrade's image with the contract its directory holds, which chalkctl
// checks the inventory's config against before any node is drained. The node checks the config it
// gets, or without one its own, against the schemas in the image itself.
func openUpgradeImage(ctx context.Context, path string, stderr io.Writer) (*diskImage, error) {
	img, err := openImage(ctx, path, false, stderr)
	if err != nil {
		return nil, err
	}
	c, err := imageContract(filepath.Dir(img.definitions))
	if err != nil {
		img.Close()
		return nil, err
	}
	c.platform = img.Info.Platform()
	img.contract = &c
	return img, nil
}

// Close closes the image's parts; an image in a registry has none open.
func (img *diskImage) Close() error {
	if img.Parts == nil {
		return nil
	}
	return img.Parts.Close()
}

// ociImage is an install's or an upgrade's image in a registry, which the node pulls: the header
// that its artifact's config and its store, hash-tree, UKI and, with bootLoader, boot-loader
// layers give, as openImage gives it of a directory, and the contract of its contract layer,
// which chalkctl checks the inventory's config against.
func ociImage(nc *inventory.NodeConfig, bootLoader bool) (*diskImage, error) {
	a, err := nc.Artifact()
	if err != nil {
		return nil, err
	}
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
	c, err := inventoryContract(nc)
	if err != nil {
		return nil, err
	}
	c.platform = a.Header.Platform
	return &diskImage{header: h, contract: &c, source: &nodev1.ImageSource{Source: &nodev1.ImageSource_Oci{Oci: &nodev1.RegistryImage{Reference: a.Reference}}}}, nil
}

// pullsInstall refuses to have an installer that cannot pull an image from a registry install
// the node's image in one, before chalkctl reads the image.
func pullsInstall(nc *inventory.NodeConfig, info *nodev1.InfoResponse) error {
	if nc.ImageOCI == "" || info.GetPullsInstallImages() {
		return nil
	}
	return fmt.Errorf("%s runs an installer that cannot pull an image from a registry, so it cannot install the image %s from %s; boot an installer built from this chalkos, or pass --image with the image's directory", nc.Name, nc.ImageName, nc.ImageOCI)
}

// size is how many bytes of the image follow its header.
func (img *diskImage) size() uint64 {
	h := img.header
	return h.Store.GetSize() + h.HashTree.GetSize() + h.Uki.GetSize() + h.BootLoader.GetSize()
}

// stream is the image's store, hash tree, UKI and boot loader as install and upgrade send them.
func (img *diskImage) stream() (io.Reader, func(), error) {
	u, err := os.Open(img.UKI)
	if err != nil {
		return nil, nil, err
	}
	data, tree := img.Store.Data, img.Store.HashTree
	readers := []io.Reader{io.NewSectionReader(data, 0, data.Size()), io.NewSectionReader(tree, 0, tree.Size()), u}
	closers := []io.Closer{u}
	if img.BootLoader != "" {
		b, err := os.Open(img.BootLoader)
		if err != nil {
			u.Close()
			return nil, nil, err
		}
		readers = append(readers, b)
		closers = append(closers, b)
	}
	return io.MultiReader(readers...), func() {
		for _, c := range closers {
			c.Close()
		}
	}, nil
}

// checkInstallImage checks that the image is built for the platform of the node's config's image.
// Its variant may be any: the installer checks the config against the image's schemas.
func checkInstallImage(t *target, img *diskImage) error {
	want, err := t.cluster.platform(t.name)
	if err != nil {
		return err
	}
	if got := img.header.GetPlatform(); got != want {
		return fmt.Errorf("%s runs on %s; the image is built for %s", t.name, want, got)
	}
	return nil
}

// sendChunks sends what r holds in chunks. A send that finds the stream closed ends it: the node
// refused the image already, and closing the stream returns why.
func sendChunks(r io.Reader, send func(*nodev1.ImageChunk) error) error {
	buf := make([]byte, chunkSize)
	for {
		n, rerr := io.ReadFull(r, buf)
		if n > 0 {
			if err := send(&nodev1.ImageChunk{Data: buf[:n]}); errors.Is(err, io.EOF) {
				return nil
			} else if err != nil {
				return err
			}
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// provenance is the revision and tag the metadata.json in dir names for the image of the root
// hash. Ignoring metadata that is there draws a warning on stderr, as the operator likely meant
// it to count; an image without metadata draws none.
func provenance(dir string, rootHash []byte, stderr io.Writer) (revision, tag string) {
	revision, tag, err := image.Provenance(dir, rootHash)
	if err != nil {
		fmt.Fprintf(stderr, "chalkctl: warning: ignoring %s, so the image is sent without a revision and tag: %v\n", filepath.Join(dir, "metadata.json"), err)
	}
	return revision, tag
}
