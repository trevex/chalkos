// Package ociimage reads and writes chalkos images as OCI artifacts: one image manifest of the
// artifact type application/vnd.chalkos.image.v1, whose config is the image's header and whose
// layers are its store, hash tree, UKI, boot loader and contract, stored uncompressed. A layer is
// found by its media type, not its position.
package ociimage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// ArtifactType is the artifact type of a chalkos image's manifest.
const ArtifactType = "application/vnd.chalkos.image.v1"

// The media types of a chalkos image's config and layers.
const (
	HeaderType     types.MediaType = "application/vnd.chalkos.image.header.v1+json"
	StoreType      types.MediaType = "application/vnd.chalkos.image.store.v1"
	HashTreeType   types.MediaType = "application/vnd.chalkos.image.hash-tree.v1"
	UKIType        types.MediaType = "application/vnd.chalkos.image.uki.v1"
	BootLoaderType types.MediaType = "application/vnd.chalkos.image.boot-loader.v1"
	ContractType   types.MediaType = "application/vnd.chalkos.image.contract.v1.tar"
)

// layerTypes are the media types of an image's layers, in the order Build writes them.
var layerTypes = []types.MediaType{StoreType, HashTreeType, UKIType, BootLoaderType, ContractType}

// Header is a chalkos image's config: what identifies the image and what it runs on.
type Header struct {
	// ImageID is the image's ID, as its os-release names it.
	ImageID string `json:"imageId"`
	// RootHash is the verity root hash of the image's store, in lower-case hex.
	RootHash string `json:"rootHash"`
	// Architecture is the architecture the image runs on, as systemd names it: x86-64 or arm64.
	Architecture string `json:"architecture"`
	// Platform is the platform the image is built for, such as metal or kvm.
	Platform string `json:"platform"`
	// Revision and Tag are the git revision and tag the image was built from, as its
	// metadata.json names them; empty when the build named none.
	Revision string `json:"revision"`
	Tag      string `json:"tag"`
}

// rootHash is a store's root hash as a header writes it.
var rootHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

// check refuses a header that names no image ID, architecture or platform, or no root hash of 64
// lower-case hex digits.
func (h Header) check() error {
	switch {
	case h.ImageID == "" || h.Architecture == "" || h.Platform == "":
		return fmt.Errorf("its header names no image ID, architecture or platform: %+v", h)
	case !rootHash.MatchString(h.RootHash):
		return fmt.Errorf("its header names the root hash %q, which is not 64 lower-case hex digits", h.RootHash)
	}
	return nil
}

// Blob is a layer's content: its media type, size and digest, and how to read it.
type Blob struct {
	MediaType types.MediaType
	Size      int64
	Digest    v1.Hash
	Open      func() (io.ReadCloser, error)
}

// NewBlob is the blob of the media type that open reads; NewBlob reads it once for its size and
// digest.
func NewBlob(mediaType types.MediaType, open func() (io.ReadCloser, error)) (Blob, error) {
	r, err := open()
	if err != nil {
		return Blob{}, err
	}
	defer r.Close()
	digest, size, err := v1.SHA256(r)
	if err != nil {
		return Blob{}, err
	}
	return Blob{MediaType: mediaType, Size: size, Digest: digest, Open: open}, nil
}

// BytesBlob is the blob of data.
func BytesBlob(mediaType types.MediaType, data []byte) Blob {
	digest, size, _ := v1.SHA256(bytes.NewReader(data))
	return Blob{MediaType: mediaType, Size: size, Digest: digest, Open: func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}}
}

// FileBlob is the blob of a file.
func FileBlob(mediaType types.MediaType, path string) (Blob, error) {
	return NewBlob(mediaType, func() (io.ReadCloser, error) { return os.Open(path) })
}

// SectionBlob is the blob of a section of a file, such as a raw image's store.
func SectionBlob(mediaType types.MediaType, s *io.SectionReader) (Blob, error) {
	return NewBlob(mediaType, func() (io.ReadCloser, error) {
		return io.NopCloser(io.NewSectionReader(s, 0, s.Size())), nil
	})
}

// Build is the chalkos image of the header and the blobs of its store, hash tree, UKI, boot
// loader and contract.
func Build(h Header, store, hashTree, uki, bootLoader, contract Blob) (v1.Image, error) {
	if err := h.check(); err != nil {
		return nil, fmt.Errorf("the image: %w", err)
	}
	config, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	return build(config, []Blob{store, hashTree, uki, bootLoader, contract})
}

// Replace is img with the blobs in place of its layers of their media types; its config and its
// other layers stay as they are. It refuses an img whose manifest holds a layer besides the five
// known ones, rather than silently dropping it.
func Replace(img v1.Image, blobs ...Blob) (v1.Image, error) {
	if _, err := ReadHeader(img); err != nil {
		return nil, err
	}
	m, err := img.Manifest()
	if err != nil {
		return nil, err
	}
	if len(m.Layers) != len(layerTypes) {
		return nil, fmt.Errorf("it has %d layers, not the %d of a chalkos image", len(m.Layers), len(layerTypes))
	}
	config, err := img.RawConfigFile()
	if err != nil {
		return nil, err
	}
	layers := make([]Blob, len(layerTypes))
	for i, mt := range layerTypes {
		if j := slices.IndexFunc(blobs, func(b Blob) bool { return b.MediaType == mt }); j >= 0 {
			layers[i] = blobs[j]
			continue
		}
		l, err := Layer(img, mt)
		if err != nil {
			return nil, err
		}
		digest, err := l.Digest()
		if err != nil {
			return nil, err
		}
		size, err := l.Size()
		if err != nil {
			return nil, err
		}
		layers[i] = Blob{MediaType: mt, Size: size, Digest: digest, Open: l.Compressed}
	}
	return build(config, layers)
}

// build is the image of the config and the layers, of the media types of layerTypes in order.
func build(config []byte, layers []Blob) (v1.Image, error) {
	configDigest, configSize, err := v1.SHA256(bytes.NewReader(config))
	if err != nil {
		return nil, err
	}
	m := v1.Manifest{
		SchemaVersion: 2,
		MediaType:     types.OCIManifestSchema1,
		ArtifactType:  ArtifactType,
		Config:        v1.Descriptor{MediaType: HeaderType, Size: configSize, Digest: configDigest},
	}
	a := &artifact{config: config, blobs: map[v1.Hash]Blob{}}
	for i, b := range layers {
		if b.MediaType != layerTypes[i] {
			return nil, fmt.Errorf("layer %d is of the media type %s, not %s", i, b.MediaType, layerTypes[i])
		}
		if _, dup := a.blobs[b.Digest]; dup {
			return nil, fmt.Errorf("two layers share the digest %s", b.Digest)
		}
		m.Layers = append(m.Layers, v1.Descriptor{MediaType: b.MediaType, Size: b.Size, Digest: b.Digest})
		a.blobs[b.Digest] = b
	}
	if a.manifest, err = json.Marshal(m); err != nil {
		return nil, err
	}
	return partial.CompressedToImage(a)
}

// artifact is a chalkos image as go-containerregistry reads one.
type artifact struct {
	config, manifest []byte
	blobs            map[v1.Hash]Blob
}

func (a *artifact) MediaType() (types.MediaType, error) { return types.OCIManifestSchema1, nil }
func (a *artifact) RawConfigFile() ([]byte, error)      { return a.config, nil }
func (a *artifact) RawManifest() ([]byte, error)        { return a.manifest, nil }

func (a *artifact) LayerByDigest(h v1.Hash) (partial.CompressedLayer, error) {
	b, ok := a.blobs[h]
	if !ok {
		return nil, fmt.Errorf("the image has no layer %s", h)
	}
	return layer{b}, nil
}

// layer is a blob as go-containerregistry reads a layer.
type layer struct{ b Blob }

func (l layer) Digest() (v1.Hash, error)            { return l.b.Digest, nil }
func (l layer) Size() (int64, error)                { return l.b.Size, nil }
func (l layer) MediaType() (types.MediaType, error) { return l.b.MediaType, nil }
func (l layer) Compressed() (io.ReadCloser, error)  { return l.b.Open() }

// ReadHeader checks that img is a chalkos image, of the artifact type and with one layer of each
// media type, and returns its header.
func ReadHeader(img v1.Image) (Header, error) {
	m, err := img.Manifest()
	if err != nil {
		return Header{}, err
	}
	if m.ArtifactType != ArtifactType || m.Config.MediaType != HeaderType {
		return Header{}, fmt.Errorf("it is no chalkos image: its artifact type is %q and its config's media type %q, not %s and %s", m.ArtifactType, m.Config.MediaType, ArtifactType, HeaderType)
	}
	for _, mt := range layerTypes {
		if _, err := find(m, mt); err != nil {
			return Header{}, err
		}
	}
	config, err := img.RawConfigFile()
	if err != nil {
		return Header{}, err
	}
	var h Header
	if err := json.Unmarshal(config, &h); err != nil {
		return Header{}, fmt.Errorf("its header: %w", err)
	}
	if err := h.check(); err != nil {
		return Header{}, err
	}
	return h, nil
}

// Layer is the layer of img of the media type.
func Layer(img v1.Image, mediaType types.MediaType) (v1.Layer, error) {
	m, err := img.Manifest()
	if err != nil {
		return nil, err
	}
	d, err := find(m, mediaType)
	if err != nil {
		return nil, err
	}
	return img.LayerByDigest(d.Digest)
}

// find is the one layer of the manifest of the media type.
func find(m *v1.Manifest, mediaType types.MediaType) (v1.Descriptor, error) {
	var found []v1.Descriptor
	for _, l := range m.Layers {
		if l.MediaType == mediaType {
			found = append(found, l)
		}
	}
	if len(found) != 1 {
		return v1.Descriptor{}, fmt.Errorf("it has %d layers of the media type %s, not one", len(found), mediaType)
	}
	return found[0], nil
}
