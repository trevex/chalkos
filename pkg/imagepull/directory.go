package imagepull

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/trevex/chalkos/pkg/arch"
	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/imagepart"
	"github.com/trevex/chalkos/pkg/install"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/storage/gpt"
	"github.com/trevex/chalkos/pkg/storeslot"
	"github.com/trevex/chalkos/pkg/uki"
)

// rawName is the file name of an image directory's raw disk image, as an image's build names it.
const rawName = "chalkos.raw"

// The raw disk image's partitions start at 1 MiB and are sized in multiples of 4 KiB, as
// systemd-repart lays out an image.
const (
	firstOffset = 1 << 20
	grain       = 4096
)

// writeDirectory writes the image directory of the image a describes into tmp, as an image's
// build lays it out: the raw disk image, repart-output.json describing it, and the contract's
// files.
func writeDirectory(ctx context.Context, tmp string, a *ociimage.Artifact) error {
	ar, ok := arch.ByName(a.Header.Architecture)
	if !ok {
		return fmt.Errorf("its header names the architecture %q, for which chalkos builds no images", a.Header.Architecture)
	}
	root, err := hex.DecodeString(a.Header.RootHash)
	if err != nil {
		return err
	}
	parts, err := partsOf(a)
	if err != nil {
		return err
	}
	d, err := layOut(a.Contract, ar, root, parts)
	if err != nil {
		return err
	}
	if err := writeRaw(ctx, filepath.Join(tmp, rawName), a, ar, root, d, parts); err != nil {
		return err
	}
	if err := writePartitions(filepath.Join(tmp, "repart-output.json"), ar, root, d); err != nil {
		return err
	}
	if err := writeContract(tmp, a.Contract); err != nil {
		return err
	}
	// The temporary directory is the owner's alone; the image directory is readable to all, as
	// a layout is.
	return os.Chmod(tmp, 0o755)
}

// partsOf is the store, the hash tree, the UKI and the boot loader of the image a describes, in
// the order a pull fetches them, as the manifest names their sizes and SHA-256 sums.
func partsOf(a *ociimage.Artifact) ([]ociimage.Part, error) {
	var parts []ociimage.Part
	for _, mt := range []types.MediaType{ociimage.StoreType, ociimage.HashTreeType, ociimage.UKIType, ociimage.BootLoaderType} {
		d, err := a.Descriptor(mt)
		if err != nil {
			return nil, err
		}
		sum, err := hex.DecodeString(d.Digest.Hex)
		if err != nil || d.Digest.Algorithm != "sha256" {
			return nil, fmt.Errorf("its %s layer has the digest %s, which is no SHA-256", mt, d.Digest)
		}
		parts = append(parts, ociimage.Part{MediaType: mt, Part: imagepart.Part{Size: d.Size, SHA256: sum}})
	}
	return parts, nil
}

// disk is the raw disk image's partitions: the ESP and slot A's hash tree and store partitions.
type disk struct{ esp, tree, store gpt.Entry }

// layOut places the ESP and slot A's partitions one after the other from 1 MiB on, sized as the
// contract's repart definitions size them, or else to their content. Slot A's partitions are
// labelled by the root hash and carry the partition UUIDs systemd finds the store by.
func layOut(c ociimage.Contract, ar arch.Arch, root []byte, parts []ociimage.Part) (disk, error) {
	espSize, treeSize, storeSize, err := install.SlotASizes(c.Definitions, ar.Name)
	if err != nil {
		return disk{}, fmt.Errorf("its repart definitions: %w", err)
	}
	if espSize == 0 {
		return disk{}, fmt.Errorf("its repart definitions leave the ESP's size open")
	}
	store, tree := parts[0].Size, parts[1].Size
	treeSize, storeSize = sizeFor(treeSize, tree), sizeFor(storeSize, store)
	if tree > treeSize || store > storeSize {
		return disk{}, fmt.Errorf("its hash tree of %d bytes and store of %d do not fit the partitions of %d and %d bytes its repart definitions give slot A", tree, store, treeSize, storeSize)
	}
	id := storeslot.LabelID(root)
	dataUUID, verityUUID := storeslot.PartitionUUIDs(root)
	var d disk
	d.esp = gpt.Entry{Start: firstOffset, Size: espSize, Type: gpt.TypeESP, UUID: espUUID(), Name: "esp"}
	d.tree = gpt.Entry{Start: d.esp.Start + d.esp.Size, Size: treeSize, Type: ar.UsrVerity, UUID: verityUUID, Name: "store-verity_" + id, Attributes: gpt.AttrReadOnly}
	d.store = gpt.Entry{Start: d.tree.Start + d.tree.Size, Size: storeSize, Type: ar.Usr, UUID: dataUUID, Name: "store_" + id, Attributes: gpt.AttrReadOnly}
	return d, nil
}

// sizeFor is the size of a partition its definition fixes, or else the size of its content in
// multiples of 4 KiB, as systemd-repart minimises it.
func sizeFor(fixed, content int64) int64 {
	if fixed != 0 {
		return fixed
	}
	return (content + grain - 1) / grain * grain
}

// writeRaw writes the raw disk image of the image a describes to the new file path: the GPT of
// d, the store and hash tree pulled into slot A and checked against each other, and the ESP
// formatted as FAT32 with the pulled boot loader and the UKI as the first generation.
func writeRaw(ctx context.Context, path string, a *ociimage.Artifact, ar arch.Arch, root []byte, d disk, parts []ociimage.Part) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	size := gpt.TableSize(d.store.Start + d.store.Size)
	if err := f.Truncate(size); err != nil {
		return err
	}
	if err := gpt.WriteTable(f, size, diskUUID(), []gpt.Entry{d.esp, d.tree, d.store}); err != nil {
		return err
	}
	sources, err := sourcesOf(a.Reference)
	if err != nil {
		return err
	}
	pull, err := ociimage.NewPull(ctx, a.Reference, sources, a.Header, parts)
	if err != nil {
		return err
	}
	defer pull.Close()
	if err := parts[0].Receive(io.NewOffsetWriter(f, d.store.Start), pull, "store"); err != nil {
		return err
	}
	if err := parts[1].Receive(io.NewOffsetWriter(f, d.tree.Start), pull, "hash tree"); err != nil {
		return err
	}
	binaries, err := os.MkdirTemp("", "chalkos-pull-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(binaries)
	ukiPath, loaderPath := filepath.Join(binaries, "uki.efi"), filepath.Join(binaries, "boot-loader.efi")
	if err := receiveFile(ukiPath, parts[2].Part, pull, "UKI"); err != nil {
		return err
	}
	if err := receiveFile(loaderPath, parts[3].Part, pull, "boot loader"); err != nil {
		return err
	}
	if err := checkStore(io.NewSectionReader(f, d.store.Start, d.store.Size), io.NewSectionReader(f, d.tree.Start, d.tree.Size),
		parts[0].Size, parts[1].Size, a.Header.RootHash); err != nil {
		return err
	}
	if err := checkBinaries(ukiPath, loaderPath, a.Header, root); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return image.WriteESP(path, d.esp.Start, d.esp.Size, espVolumeID(d.esp.UUID), []image.ESPFile{
		{From: loaderPath, To: "/EFI/BOOT/" + ar.BootLoader},
		{From: ukiPath, To: "/EFI/Linux/" + a.Header.ImageID + "_1.efi"},
	})
}

// sourcesOf is the registry of ref, with the credentials the default keychain holds for it, which
// it reaches over plain HTTP where ociimage.Read does: on a loopback or private address.
func sourcesOf(ref string) ([]ociimage.Source, error) {
	d, err := name.NewDigest(ref, name.StrictValidation)
	if err != nil {
		return nil, err
	}
	auth, err := authn.DefaultKeychain.Resolve(d.Context())
	if err != nil {
		return nil, err
	}
	return []ociimage.Source{{Auth: auth, Insecure: d.Context().Registry.Scheme() == "http"}}, nil
}

// receiveFile receives a part from r into the new file path.
func receiveFile(path string, p imagepart.Part, r io.Reader, what string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	err = p.Receive(f, r, what)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// checkBinaries refuses a UKI or a boot loader that is not built for the header's architecture,
// and a UKI that boots another store or describes another image than the header, as an image
// directory's reader would.
func checkBinaries(ukiPath, loaderPath string, h ociimage.Header, root []byte) error {
	read := func(path string, use func(r io.ReaderAt) error) error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		return use(f)
	}
	for _, b := range []struct{ path, what string }{{ukiPath, "UKI"}, {loaderPath, "boot loader"}} {
		err := read(b.path, func(r io.ReaderAt) error {
			got, err := uki.Architecture(r)
			if err == nil && got != h.Architecture {
				err = fmt.Errorf("it is built for %s, not %s", got, h.Architecture)
			}
			return err
		})
		if err != nil {
			return fmt.Errorf("its %s: %w", b.what, err)
		}
	}
	return read(ukiPath, func(r io.ReaderAt) error {
		info, err := uki.Read(r)
		if err != nil {
			return fmt.Errorf("its UKI: %w", err)
		}
		switch hash, err := info.UsrHash(); {
		case err != nil || !slices.Equal(hash, root):
			return fmt.Errorf("its UKI boots another store than the image holds")
		case info.ID() != h.ImageID || info.Platform() != h.Platform:
			return fmt.Errorf("its UKI's os-release names the image ID %q and the platform %q, but its header %q and %q", info.ID(), info.Platform(), h.ImageID, h.Platform)
		case info.Name() == "":
			return fmt.Errorf("its UKI's os-release names no image name; it is no node image")
		}
		return nil
	})
}

// partition is an entry of repart-output.json, as systemd-repart writes it for an image.
type partition struct {
	Type     string  `json:"type"`
	Label    string  `json:"label"`
	UUID     string  `json:"uuid"`
	Number   int     `json:"partno"`
	Offset   int64   `json:"offset"`
	RawSize  int64   `json:"raw_size"`
	RootHash *string `json:"roothash"`
}

// writePartitions writes repart-output.json of the ESP and slot A's hash tree and store.
func writePartitions(path string, ar arch.Arch, root []byte, d disk) error {
	rootHash := hex.EncodeToString(root)
	out := []partition{
		{Type: "esp", Label: d.esp.Name, UUID: d.esp.UUID, Number: 0, Offset: d.esp.Start, RawSize: d.esp.Size},
		{Type: "usr-" + ar.Name + "-verity", Label: d.tree.Name, UUID: d.tree.UUID, Number: 1, Offset: d.tree.Start, RawSize: d.tree.Size, RootHash: &rootHash},
		{Type: "usr-" + ar.Name, Label: d.store.Name, UUID: d.store.UUID, Number: 2, Offset: d.store.Start, RawSize: d.store.Size, RootHash: &rootHash},
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// writeContract writes the contract's files as an image directory holds them: metadata.json,
// schemas/, repart.d/ and facts/.
func writeContract(dir string, c ociimage.Contract) error {
	files := map[string][]byte{"metadata.json": c.Metadata}
	for name, data := range c.Schemas {
		files[filepath.Join("schemas", name)] = data
	}
	for name, data := range c.Definitions {
		files[filepath.Join("repart.d", name)] = data
	}
	for layer, data := range c.Facts {
		files[filepath.Join("facts", layer+".json")] = data
	}
	for _, sub := range []string{"schemas", "repart.d", "facts"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if err := os.WriteFile(filepath.Join(dir, name), files[name], 0o644); err != nil {
			return err
		}
	}
	return nil
}
