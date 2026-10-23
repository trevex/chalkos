package ociimage

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/trevex/chalkos/pkg/schema"
)

// manifestTimeout bounds the fetch of a pulled image's manifest and header; its layers take as
// long as their reader reads them, as long as the registry sends data.
const manifestTimeout = 2 * time.Minute

// stallTimeout is how long a layer's fetch may wait for data from the registry before it is
// canceled.
var stallTimeout = 2 * time.Minute

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
// and SHA-256 as its digest. A layer is fetched once the one before it was read to its end, and
// ends after exactly its part's size, once go-containerregistry checked its digest. A layer whose
// registry sends no data for two minutes is canceled. Every error names the reference.
//
// Contract fetches the manifest, the header and the contract layer up front, as an install needs
// them before it reads a part.
type Pull struct {
	ctx    context.Context
	ref    name.Digest
	puller *remote.Puller
	want   Header
	parts  []Part
	// next is the part read next; checked is set once the manifest was checked.
	next    int
	checked bool
	// cur is the layer being read, left the bytes of it not read yet; cancel cancels its fetch
	// with a cause, and stall does once the registry sent no data for stallTimeout.
	cur     io.ReadCloser
	left    int64
	cancel  context.CancelCauseFunc
	stall   *time.Timer
	ctxCur  context.Context
	stalled error
	// err is the error a read failed with, which every later read returns.
	err error
}

// NewPull is the Pull of the parts of the image at ref, which must name a manifest by digest,
// authenticated with auth; authn.Anonymous pulls without credentials. It pulls over HTTPS alone,
// unless insecure allows plain HTTP to ref's registry. ctx bounds every fetch.
func NewPull(ctx context.Context, ref string, auth authn.Authenticator, insecure bool, want Header, parts []Part) (*Pull, error) {
	opts := []name.Option{name.StrictValidation}
	if insecure {
		opts = append(opts, name.Insecure)
	}
	d, err := name.NewDigest(ref, opts...)
	if err != nil {
		return nil, fmt.Errorf("%q is no image reference by digest, repository@sha256:…: %w", ref, err)
	}
	var transport http.RoundTripper = remote.DefaultTransport
	if !insecure {
		transport = httpsOnly{next: transport}
	}
	p, err := remote.NewPuller(remote.WithAuth(auth), remote.WithTransport(transport))
	if err != nil {
		return nil, err
	}
	return &Pull{ctx: ctx, ref: d, puller: p, want: want, parts: parts}, nil
}

// httpsOnly refuses every request but one over HTTPS, as go-containerregistry falls back to plain
// HTTP for a loopback or private registry host, and follows redirects to any scheme.
type httpsOnly struct{ next http.RoundTripper }

func (t httpsOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" {
		return nil, fmt.Errorf("plain HTTP to %s is refused: set insecure: true in the Registry of the host to allow it", r.URL.Host)
	}
	return t.next.RoundTrip(r)
}

func (p *Pull) Read(b []byte) (int, error) {
	if p.err != nil {
		return 0, p.err
	}
	n, err := p.read(b)
	if err != nil && err != io.EOF {
		p.err = err
	}
	return n, err
}

// read is Read without the error of an earlier read.
func (p *Pull) read(b []byte) (int, error) {
	if p.cur == nil {
		if p.next == len(p.parts) {
			return 0, io.EOF
		}
		if err := p.open(); err != nil {
			return 0, err
		}
	}
	part := p.parts[p.next]
	if int64(len(b)) > p.left {
		b = b[:p.left]
	}
	p.stall.Reset(stallTimeout)
	n, err := p.cur.Read(b)
	p.left -= int64(n)
	if p.left == 0 && err == nil {
		err = p.end()
	}
	p.stall.Stop()
	switch {
	case err == io.EOF && p.left == 0:
	case err == io.EOF:
		return n, fmt.Errorf("pull %s: the %s layer ends after %d of %d bytes", p.ref, part.MediaType, part.Size-p.left, part.Size)
	case err != nil:
		if errors.Is(err, errTooLong) {
			return n, fmt.Errorf("pull %s: the %s layer is longer than %d bytes", p.ref, part.MediaType, part.Size)
		}
		if context.Cause(p.ctxCur) == p.stalled {
			return n, p.stalled
		}
		return n, fmt.Errorf("pull %s: the %s layer: %w", p.ref, part.MediaType, err)
	default:
		return n, nil
	}
	p.Close()
	p.next++
	return n, nil
}

// errTooLong is the error of a layer longer than its part.
var errTooLong = errors.New("the layer is longer than its part")

// end reads the layer being read past the bytes of its part and returns io.EOF if it ends there,
// which go-containerregistry returns only once the layer's digest matched.
func (p *Pull) end() error {
	var one [1]byte
	for {
		n, err := p.cur.Read(one[:])
		if n > 0 {
			return errTooLong
		}
		if err != nil {
			return err
		}
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
	p.stall.Stop()
	err := p.cur.Close()
	p.cancel(nil)
	p.cur, p.cancel, p.stall, p.ctxCur, p.stalled = nil, nil, nil, nil, nil
	return err
}

// open checks the image once and opens the layer of the next part, whose fetch is canceled once
// the registry sends no data for stallTimeout.
func (p *Pull) open() error {
	if !p.checked {
		if err := p.check(); err != nil {
			return fmt.Errorf("pull %s: %w", p.ref, err)
		}
		p.checked = true
	}
	part := p.parts[p.next]
	ctx, cancel := context.WithCancelCause(p.ctx)
	stalled := fmt.Errorf("no data from the registry for %v while pulling the %s layer of %s", stallTimeout, part.MediaType, p.ref)
	stall := time.AfterFunc(stallTimeout, func() { cancel(stalled) })
	l, err := p.puller.Layer(ctx, p.ref.Context().Digest("sha256:"+hex.EncodeToString(part.SHA256)))
	var rc io.ReadCloser
	if err == nil {
		rc, err = l.Compressed()
	}
	stall.Stop()
	if err != nil {
		if context.Cause(ctx) == stalled {
			cancel(nil)
			return stalled
		}
		cancel(nil)
		return fmt.Errorf("pull %s: the %s layer: %w", p.ref, part.MediaType, err)
	}
	p.cur, p.left, p.cancel, p.stall, p.ctxCur, p.stalled = rc, part.Size, cancel, stall, ctx, stalled
	return nil
}

// check fetches the manifest and the header and refuses them as image does, within
// manifestTimeout.
func (p *Pull) check() error {
	ctx, cancel := context.WithTimeout(p.ctx, manifestTimeout)
	defer cancel()
	_, err := p.image(ctx)
	return err
}

// image fetches the manifest, by its digest, and the header with ctx, and refuses an image of
// another header than the one wanted, or whose layer of a part's media type has another size or
// digest than the part. Its layers are fetched with ctx too.
func (p *Pull) image(ctx context.Context) (v1.Image, error) {
	desc, err := p.puller.Get(ctx, p.ref)
	if err != nil {
		return nil, err
	}
	if !desc.MediaType.IsImage() {
		return nil, fmt.Errorf("it is a %s, not an image manifest; a chalkos image is one manifest", desc.MediaType)
	}
	img, err := desc.Image()
	if err != nil {
		return nil, err
	}
	h, err := ReadHeader(img)
	if err != nil {
		return nil, err
	}
	if h != p.want {
		return nil, fmt.Errorf("its header describes %+v, but the image to install is %+v", h, p.want)
	}
	m, err := img.Manifest()
	if err != nil {
		return nil, err
	}
	for _, part := range p.parts {
		d, err := find(m, part.MediaType)
		if err != nil {
			return nil, err
		}
		if want := "sha256:" + hex.EncodeToString(part.SHA256); d.Digest.String() != want || d.Size != part.Size {
			return nil, fmt.Errorf("its %s layer is %s of %d bytes, but the image to install has %s of %d", part.MediaType, d.Digest, d.Size, want, part.Size)
		}
	}
	return img, nil
}

// Contract fetches the manifest and the header, refusing them as the first read does, and reads
// the image's contract layer, refusing one whose metadata.json describes another image than the
// header. Reads after it fetch the manifest no more.
func (p *Pull) Contract() (Contract, error) {
	ctx, cancel := context.WithTimeout(p.ctx, manifestTimeout)
	defer cancel()
	img, err := p.image(ctx)
	if err != nil {
		return Contract{}, fmt.Errorf("pull %s: %w", p.ref, err)
	}
	p.checked = true
	// The remote image fetches its layers with the context of the manifest's fetch, so the
	// contract layer is read before that context ends.
	c, err := ReadContract(img)
	if err != nil {
		return Contract{}, fmt.Errorf("pull %s: %w", p.ref, err)
	}
	md, err := schema.ParseMetadata(c.Metadata)
	if err != nil {
		return Contract{}, fmt.Errorf("pull %s: the contract layer's %w", p.ref, err)
	}
	if err := describes(md, p.want); err != nil {
		return Contract{}, fmt.Errorf("pull %s: the contract layer's %w", p.ref, err)
	}
	return c, nil
}
