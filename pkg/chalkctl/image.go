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
	"golang.org/x/term"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/inventory"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/sdk"
	"github.com/trevex/chalkos/pkg/statusview"
)

// diskImage is what install and upgrade send of a disk image: its parts, the UKI and the boot
// loader, and the header that describes them to the nodes.
type diskImage struct {
	*image.Parts
	// header describes the image to the nodes.
	header *nodev1.ImageHeader
	// contract is what the image accepts as config, as its directory or its contract layer says,
	// which chalkctl checks the inventory's config against.
	contract *sdk.Contract
	// definitions is the directory of the image's repart definitions of its system region.
	definitions string
	// source is where the node takes the image's parts from: nil when chalkctl streams them,
	// an image in a registry when the node pulls them.
	source *nodev1.ImageSource
	// path is the image directory or raw image the image was read from; empty for an image in a
	// registry.
	path string
}

// openImage reads a raw image with repart-output.json beside it, or nix build's directory, with its
// contract, for install (with bootLoader) or upgrade. Warnings go to stderr.
func openImage(ctx context.Context, path string, bootLoader bool, stderr io.Writer) (_ *diskImage, err error) {
	p, raw, definitions, err := openParts(path, bootLoader)
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
	if err := img.Info.CheckImageVersion(); err != nil {
		return nil, fmt.Errorf("the image's UKI: %w", err)
	}
	if img.Info.Name() == "" || img.Info.Platform() == "" {
		return nil, errors.New("the image names no image name or platform in its os-release; it is no node image")
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
	c, err := imageContract(filepath.Dir(raw))
	if err != nil {
		return nil, err
	}
	c.Platform = img.Info.Platform()
	img.contract = &c
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

// Close closes the image's parts; an image in a registry has none open.
func (img *diskImage) Close() error {
	if img.Parts == nil {
		return nil
	}
	return img.Parts.Close()
}

// ociImage is openImage for an image in a registry, which the node pulls: the header comes from its
// artifact's config and layers, the contract from its contract layer.
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
	c, err := inventoryContract(nc, a.Header.Platform)
	if err != nil {
		return nil, err
	}
	return &diskImage{header: h, contract: &c, source: &nodev1.ImageSource{Source: &nodev1.ImageSource_Oci{Oci: &nodev1.RegistryImage{Reference: a.Reference}}}}, nil
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
// Its name may be any: the installer checks the config against the image's schemas.
func checkInstallImage(t *target, img *diskImage) error {
	_, md, err := t.cluster.image(t.name)
	if err != nil {
		return err
	}
	if got := img.header.GetPlatform(); got != md.Platform {
		return fmt.Errorf("%s runs on %s; the image is built for %s", t.name, md.Platform, got)
	}
	return nil
}

// sendChunks sends what r holds in chunks and reports to progress, unless nil, how much it sent. A
// send that finds the stream closed ends it: the node refused the image already, and closing the
// stream returns why.
func sendChunks(r io.Reader, send func(*nodev1.ImageChunk) error, progress func(sent uint64)) error {
	buf := make([]byte, chunkSize)
	var sent uint64
	for {
		n, rerr := io.ReadFull(r, buf)
		if n > 0 {
			if err := send(&nodev1.ImageChunk{Data: buf[:n]}); errors.Is(err, io.EOF) {
				return nil
			} else if err != nil {
				return err
			}
			sent += uint64(n)
			if progress != nil {
				progress(sent)
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

// provenance is the revision and tag dir's metadata.json names for the image of the root hash.
// Metadata that is there but ignored draws a warning on stderr; no metadata draws none.
func provenance(dir string, rootHash []byte, stderr io.Writer) (revision, tag string) {
	revision, tag, err := image.Provenance(dir, rootHash)
	if err != nil {
		fmt.Fprintf(stderr, "chalkctl: warning: ignoring %s, so the image has no revision and tag: %v\n", filepath.Join(dir, "metadata.json"), err)
	}
	return revision, tag
}

// chunkSize is how much of an image one install or upgrade message carries.
const chunkSize = 1 << 20

// openParts opens the parts of the raw image at path, or of an image directory's, as its
// repart-output.json lays them out; definitions is the image's repart.d beside it.
func openParts(path string, bootLoader bool) (p *image.Parts, raw, definitions string, err error) {
	if raw, err = image.FindRaw(path); err != nil {
		return nil, "", "", err
	}
	parts, err := image.ReadPartitions(filepath.Join(filepath.Dir(raw), "repart-output.json"))
	if err != nil {
		return nil, "", "", err
	}
	if p, err = image.OpenParts(raw, parts, bootLoader); err != nil {
		return nil, "", "", err
	}
	return p, raw, filepath.Join(filepath.Dir(raw), "repart.d"), nil
}

// name names the image chalkctl sends.
func (img *diskImage) name() string {
	return statusview.ImageName(img.header.GetRootHash(), img.header.GetRevision(), img.header.GetTag())
}

// binarySize is a number of bytes in MiB, or in GiB from 1 GiB on, with one decimal.
func binarySize(n uint64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

// sendProgress is what sendChunks reports to while it sends the node called name total bytes: a
// line through say about each tenth of total, while standard output is a terminal. A log, as of
// CI, gets no progress lines; nil reports nothing.
func (a *app) sendProgress(say func(format string, args ...any), name string, total uint64) func(sent uint64) {
	if !isTerminal(a.stdout) {
		return nil
	}
	return tenths(say, name, total)
}

// tenths reports a line through say each time what was sent of total passes another tenth of it,
// short of the whole.
func tenths(say func(format string, args ...any), name string, total uint64) func(sent uint64) {
	if total == 0 {
		return nil
	}
	var said uint64
	return func(sent uint64) {
		tenth := min(sent, total) * 10 / total
		if tenth <= said || tenth >= 10 {
			return
		}
		said = tenth
		say("%s: sent %d%%, %s of %s", name, tenth*10, binarySize(sent), binarySize(total))
	}
}

// isTerminal reports whether w is a terminal.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
