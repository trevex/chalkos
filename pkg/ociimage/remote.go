package ociimage

import (
	"context"
	"fmt"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

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

// options are a registry request's options: ctx, and the credentials of the default keychain,
// docker's config and its credential helpers. go-containerregistry reaches a registry over plain
// HTTP at localhost with a port, at a name ending in .localhost, at 127.0.0.1, at ::1, or at a
// private (RFC 1918) address, and any other registry over HTTPS.
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
	if !desc.MediaType.IsImage() {
		return nil, fmt.Errorf("%s is a %s, not an image manifest; a chalkos image is one manifest", ref, desc.MediaType)
	}
	img, err := desc.Image()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	a := &Artifact{Reference: r.Context().Digest(desc.Digest.String()).String()}
	if a.Header, err = ReadHeader(img); err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	if a.Contract, err = ReadContract(img); err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	if a.Metadata, err = schema.ParseMetadata(a.Contract.Metadata); err != nil {
		return nil, fmt.Errorf("%s: the contract layer's %w", ref, err)
	}
	if err := describes(a.Metadata, a.Header); err != nil {
		return nil, fmt.Errorf("%s: the contract layer's %w", ref, err)
	}
	return a, nil
}
