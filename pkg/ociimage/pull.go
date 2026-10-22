package ociimage

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// manifestTimeout bounds the fetch of a pulled image's manifest and header; its layers take as
// long as their reader reads them.
const manifestTimeout = 2 * time.Minute

// Part is a layer an image is pulled for: its media type, and the size and SHA-256 of its content.
type Part struct {
	MediaType types.MediaType
	Size      int64
	SHA256    []byte
}

// Pull reads the parts of the chalkos image a reference names by its manifest's digest, one after
// the other, as one stream: an install or an upgrade reads it as it reads the chunks of an image
// streamed to it. The first read fetches the manifest and the header and refuses an image whose
// header is not the one wanted, or that has no layer of a part's media type with the part's size
// and SHA-256 as its digest. A layer is fetched once the one before it was read to its end. Every
// error names the reference.
type Pull struct {
	ctx    context.Context
	ref    name.Digest
	puller *remote.Puller
	want   Header
	parts  []Part
	// next is the part read next; checked is set once the manifest was checked.
	next    int
	checked bool
	cur     io.ReadCloser
}

// NewPull is the Pull of the parts of the image at ref, which must name a manifest by digest,
// authenticated with auth; authn.Anonymous pulls without credentials. ctx bounds every fetch.
func NewPull(ctx context.Context, ref string, auth authn.Authenticator, want Header, parts []Part) (*Pull, error) {
	d, err := name.NewDigest(ref, name.StrictValidation)
	if err != nil {
		return nil, fmt.Errorf("%q is no image reference by digest, repository@sha256:…: %w", ref, err)
	}
	p, err := remote.NewPuller(remote.WithAuth(auth))
	if err != nil {
		return nil, err
	}
	return &Pull{ctx: ctx, ref: d, puller: p, want: want, parts: parts}, nil
}

func (p *Pull) Read(b []byte) (int, error) {
	for {
		if p.cur == nil {
			if p.next == len(p.parts) {
				return 0, io.EOF
			}
			if err := p.open(); err != nil {
				return 0, err
			}
		}
		n, err := p.cur.Read(b)
		if err == io.EOF {
			p.cur.Close()
			p.cur = nil
			p.next++
			if n > 0 {
				return n, nil
			}
			continue
		}
		if err != nil {
			return n, fmt.Errorf("pull %s: the %s layer: %w", p.ref, p.parts[p.next].MediaType, err)
		}
		return n, nil
	}
}

// Skip passes over the next n bytes. The parts not opened yet that n spans whole are passed over
// without being fetched; the rest is read and dropped.
func (p *Pull) Skip(n int64) error {
	for p.cur == nil && p.next < len(p.parts) && p.parts[p.next].Size <= n {
		n -= p.parts[p.next].Size
		p.next++
	}
	_, err := io.CopyN(io.Discard, p, n)
	return err
}

// Close closes the layer being read.
func (p *Pull) Close() error {
	if p.cur == nil {
		return nil
	}
	err := p.cur.Close()
	p.cur = nil
	return err
}

// open checks the image once and opens the layer of the next part.
func (p *Pull) open() error {
	if !p.checked {
		if err := p.check(); err != nil {
			return fmt.Errorf("pull %s: %w", p.ref, err)
		}
		p.checked = true
	}
	part := p.parts[p.next]
	l, err := p.puller.Layer(p.ctx, p.ref.Context().Digest("sha256:"+hex.EncodeToString(part.SHA256)))
	if err == nil {
		p.cur, err = l.Compressed()
	}
	if err != nil {
		return fmt.Errorf("pull %s: the %s layer: %w", p.ref, part.MediaType, err)
	}
	return nil
}

// check fetches the manifest, by its digest, and the header, and refuses an image of another
// header than the one wanted, or whose layer of a part's media type has another size or digest
// than the part.
func (p *Pull) check() error {
	ctx, cancel := context.WithTimeout(p.ctx, manifestTimeout)
	defer cancel()
	desc, err := p.puller.Get(ctx, p.ref)
	if err != nil {
		return err
	}
	if !desc.MediaType.IsImage() {
		return fmt.Errorf("it is a %s, not an image manifest; a chalkos image is one manifest", desc.MediaType)
	}
	img, err := desc.Image()
	if err != nil {
		return err
	}
	h, err := ReadHeader(img)
	if err != nil {
		return err
	}
	if h != p.want {
		return fmt.Errorf("its header describes %+v, but the image to install is %+v", h, p.want)
	}
	m, err := img.Manifest()
	if err != nil {
		return err
	}
	for _, part := range p.parts {
		d, err := find(m, part.MediaType)
		if err != nil {
			return err
		}
		if want := "sha256:" + hex.EncodeToString(part.SHA256); d.Digest.String() != want || d.Size != part.Size {
			return fmt.Errorf("its %s layer is %s of %d bytes, but the image to install has %s of %d", part.MediaType, d.Digest, d.Size, want, part.Size)
		}
	}
	return nil
}
