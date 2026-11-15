package ociimage

import (
	"context"
	"fmt"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/trevex/chalkos/pkg/schema"
)

// ParseReference parses a reference to an image in a registry, which must name its registry and
// a tag or a digest, such as ghcr.io/example/node-metal:v1.
func ParseReference(ref string) (name.Reference, error) {
	r, err := name.ParseReference(ref, name.StrictValidation)
	if err != nil {
		return nil, fmt.Errorf("%q is no image reference with a registry and a tag or digest: %w", ref, err)
	}
	return r, nil
}

// options are the remote options of Push and Read: ctx and the default keychain.
// go-containerregistry uses plain HTTP for localhost:<port>, *.localhost, 127.0.0.1, ::1 and
// RFC 1918 addresses, each with an optional port, and HTTPS for any other registry.
func options(ctx context.Context) []remote.Option {
	return []remote.Option{remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain)}
}

// Push pushes a chalkos image to ref and returns the reference of its manifest by digest,
// repository@sha256:….
func Push(ctx context.Context, img v1.Image, ref string) (string, error) {
	r, err := ParseReference(ref)
	if err != nil {
		return "", err
	}
	if err := remote.Write(r, img, options(ctx)...); err != nil {
		return "", fmt.Errorf("push %s: %w", ref, err)
	}
	d, err := img.Digest()
	if err != nil {
		return "", err
	}
	return r.Context().Digest(d.String()).String(), nil
}

// Artifact is a chalkos image in a registry, pinned to its manifest's digest: its header, its
// contract and the contract's metadata.json.
type Artifact struct {
	// Reference is the image's repository and manifest digest, repository@sha256:….
	Reference string
	Header    Header
	Contract  Contract
	Metadata  schema.Metadata
	// Manifest is the image's manifest, whose layers' descriptors Descriptor finds.
	Manifest v1.Manifest
}

// Descriptor is the descriptor of the artifact's layer of the media type: its digest and size.
func (a *Artifact) Descriptor(mediaType types.MediaType) (v1.Descriptor, error) {
	return find(&a.Manifest, mediaType)
}

// Read resolves ref, by tag or by digest, to its manifest's digest and reads the header and the
// contract layer of the chalkos image there, with the credentials of the default keychain. It
// refuses an index, an image of another artifact type, and a contract whose metadata.json
// describes another image than the header.
func Read(ctx context.Context, ref string) (*Artifact, error) {
	r, err := ParseReference(ref)
	if err != nil {
		return nil, err
	}
	desc, err := remote.Get(r, options(ctx)...)
	if err != nil {
		return nil, err
	}
	img, err := imageOf(desc)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	a := &Artifact{Reference: r.Context().Digest(desc.Digest.String()).String()}
	if a.Header, err = ReadHeader(img); err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	if a.Contract, a.Metadata, err = contractOf(img, a.Header); err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	m, err := img.Manifest()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	a.Manifest = *m
	return a, nil
}

// imageOf is the image a descriptor names, refusing an index.
func imageOf(desc *remote.Descriptor) (v1.Image, error) {
	if !desc.MediaType.IsImage() {
		return nil, fmt.Errorf("it is a %s, not an image manifest; a chalkos image is one manifest", desc.MediaType)
	}
	return desc.Image()
}

// contractOf reads img's contract layer and refuses one whose metadata.json describes another
// image than h.
func contractOf(img v1.Image, h Header) (Contract, schema.Metadata, error) {
	c, err := ReadContract(img)
	if err != nil {
		return Contract{}, schema.Metadata{}, err
	}
	md, err := schema.ParseMetadata(c.Metadata)
	if err == nil {
		err = describes(md, h)
	}
	if err != nil {
		return Contract{}, schema.Metadata{}, fmt.Errorf("the contract layer's %w", err)
	}
	return c, md, nil
}
