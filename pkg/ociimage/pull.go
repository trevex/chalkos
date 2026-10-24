package ociimage

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/trevex/chalkos/pkg/schema"
)

// manifestTimeout bounds the fetch of a pulled image's manifest and header from all its sources;
// its layers take as long as their reader reads them, as long as their source sends data.
const manifestTimeout = 2 * time.Minute

// stallTimeout is how long a layer's fetch may wait for data from its source before it is
// canceled.
var stallTimeout = 2 * time.Minute

// sourceTimeout bounds the fetch of the manifest, the header and, for Contract, the contract
// layer from one source, after which the next source is asked; manifestTimeout bounds all
// sources together.
var sourceTimeout = 30 * time.Second

// Part is a layer an image is pulled for: its media type, and the size and SHA-256 of its content.
type Part struct {
	MediaType types.MediaType
	Size      int64
	SHA256    []byte
}

// Pull reads the parts of the chalkos image a reference names by digest as one stream, one part
// after the other, as an install or an upgrade reads a streamed image. Every error names the
// reference.
//
//   - The first read fetches the manifest and the header. Contract fetches them up front, with
//     the contract layer, as an install needs it before it reads a part. Either refuses an image
//     of another header, or without a layer of each part's media type, size and SHA-256.
//   - The image comes from the first source that serves the manifest, the header and, for
//     Contract, the contract layer: the mirrors in order, then the registry. A source that cannot
//     be reached, answers 401, 403, 404, 429 or 5xx, or gives no answer within sourceTimeout is
//     passed over. Other errors, such as a digest mismatch, fail the pull.
//   - A layer is fetched once the one before it is read to its end, starting at the source of the
//     manifest and passing over sources as the manifest does. It ends after exactly its part's
//     size, once go-containerregistry checked its digest.
//   - A layer's fetch is canceled when its source sends no data for stallTimeout. While the layer
//     is being opened, that passes the source over.
type Pull struct {
	ctx context.Context
	ref name.Digest
	// sources are where the image is fetched from, in order; src is the one that served the
	// manifest, where each layer's fetch starts.
	sources []source
	src     int
	want    Header
	parts   []Part
	// next is the part read next; checked is set once the manifest was checked.
	next    int
	checked bool
	// cur is the layer being read; nil between layers.
	cur *pullLayer
	// err is the error a read failed with, which every later read returns.
	err error
}

// pullLayer is the fetch of one layer of a Pull.
type pullLayer struct {
	rc io.ReadCloser
	// left is the number of bytes of the layer's part not read yet.
	left int64
	// ctx is the fetch's context; cancel cancels it with a cause, and stall does, with stalled,
	// once the source sent no data for stallTimeout.
	ctx     context.Context
	cancel  context.CancelCauseFunc
	stall   *time.Timer
	stalled error
}

// NewPull is the Pull of the parts of the image at ref, which must name a manifest by digest,
// from the sources in order. ctx bounds every fetch.
func NewPull(ctx context.Context, ref string, sources []Source, want Header, parts []Part) (*Pull, error) {
	d, err := name.NewDigest(ref, name.StrictValidation)
	if err != nil {
		return nil, fmt.Errorf("%q is no image reference by digest, repository@sha256:…: %w", ref, err)
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("pull %s: no source to pull from", ref)
	}
	p := &Pull{ctx: ctx, ref: d, want: want, parts: parts}
	for _, s := range sources {
		src, err := newSource(d, s)
		if err != nil {
			return nil, fmt.Errorf("pull %s: %w", ref, err)
		}
		p.sources = append(p.sources, src)
	}
	return p, nil
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
	part, l := p.parts[p.next], p.cur
	if int64(len(b)) > l.left {
		b = b[:l.left]
	}
	l.stall.Reset(stallTimeout)
	n, err := l.rc.Read(b)
	l.left -= int64(n)
	if l.left == 0 && err == nil {
		err = l.end()
	}
	l.stall.Stop()
	switch {
	case err == io.EOF && l.left == 0:
	case err == io.EOF:
		return n, fmt.Errorf("pull %s: the %s layer ends after %d of %d bytes", p.ref, part.MediaType, part.Size-l.left, part.Size)
	case err != nil:
		if errors.Is(err, errTooLong) {
			return n, fmt.Errorf("pull %s: the %s layer is longer than %d bytes", p.ref, part.MediaType, part.Size)
		}
		if context.Cause(l.ctx) == l.stalled {
			return n, l.stalled
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

// end reads the layer past the bytes of its part and returns io.EOF if it ends there,
// which go-containerregistry returns only once the layer's digest matched.
func (l *pullLayer) end() error {
	var one [1]byte
	for {
		n, err := l.rc.Read(one[:])
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
	l := p.cur
	p.cur = nil
	l.stall.Stop()
	err := l.rc.Close()
	l.cancel(nil)
	return err
}

// open checks the image once and opens the layer of the next part, whose fetch is canceled once
// its source sends no data for stallTimeout.
func (p *Pull) open() error {
	if !p.checked {
		if err := p.check(); err != nil {
			return fmt.Errorf("pull %s: %w", p.ref, err)
		}
		p.checked = true
	}
	part := p.parts[p.next]
	var passed []string
	for i := p.src; i < len(p.sources); i++ {
		s := p.sources[i]
		ctx, cancel := context.WithCancelCause(p.ctx)
		stalled := fmt.Errorf("no data from %s for %v while pulling the %s layer of %s", s.name, stallTimeout, part.MediaType, p.ref)
		stall := time.AfterFunc(stallTimeout, func() { cancel(stalled) })
		l, err := s.puller.Layer(ctx, s.ref.Context().Digest("sha256:"+hex.EncodeToString(part.SHA256)))
		var rc io.ReadCloser
		if err == nil {
			rc, err = l.Compressed()
		}
		stall.Stop()
		if err == nil {
			p.cur = &pullLayer{rc: rc, left: part.Size, ctx: ctx, cancel: cancel, stall: stall, stalled: stalled}
			return nil
		}
		if context.Cause(ctx) == stalled {
			err = stalled
		}
		cancel(nil)
		if i < len(p.sources)-1 && p.ctx.Err() == nil && (err == stalled || passable(err)) {
			passed = append(passed, passedOver(s, err))
			continue
		}
		if err == stalled {
			return after(stalled, passed)
		}
		return fmt.Errorf("pull %s: the %s layer: %w", p.ref, part.MediaType, after(err, passed))
	}
	return fmt.Errorf("pull %s: no source to pull from", p.ref)
}

// check fetches the manifest and the header and refuses them as image does.
func (p *Pull) check() error {
	return p.image(nil)
}

// image fetches the manifest, by its digest, and the header from the first source that serves
// them, within manifestTimeout and sourceTimeout per source, and refuses an image of another
// header than the one wanted, or whose layer of a part's media type has another size or digest
// than the part. It calls use, unless nil, with the image while that source's fetch lasts, as the
// remote image fetches its layers with it. A source is passed over when a fetch, use's included,
// fails as passable says or takes longer than sourceTimeout; a refusal fails at once. The layers
// then start at the source the image came from.
func (p *Pull) image(use func(v1.Image) error) error {
	ctx, cancel := context.WithTimeout(p.ctx, manifestTimeout)
	defer cancel()
	var passed []string
	for i, s := range p.sources {
		sctx, scancel := context.WithTimeout(ctx, sourceTimeout)
		err := p.imageFrom(sctx, s, use)
		timedOut := err != nil && sctx.Err() != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded)
		scancel()
		if err == nil {
			p.src = i
			return nil
		}
		if timedOut {
			err = fmt.Errorf("no answer from %s within %v: %w", s.name, sourceTimeout, err)
		}
		if i == len(p.sources)-1 || ctx.Err() != nil || !(timedOut || passable(err)) {
			return after(err, passed)
		}
		passed = append(passed, passedOver(s, err))
	}
	return errors.New("no source to pull from")
}

// imageFrom is image of one source, fetched with ctx.
func (p *Pull) imageFrom(ctx context.Context, s source, use func(v1.Image) error) error {
	desc, err := s.puller.Get(ctx, s.ref)
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
	if use == nil {
		return nil
	}
	return use(img)
}

// Contract fetches the manifest and the header, refusing them as the first read does, and reads
// the image's contract layer, refusing one whose metadata.json describes another image than the
// header. A source the contract layer cannot be fetched from is passed over as image says. Reads
// after it succeeded fetch the manifest no more.
func (p *Pull) Contract() (Contract, error) {
	var c Contract
	err := p.image(func(img v1.Image) error {
		var err error
		if c, err = ReadContract(img); err != nil {
			return err
		}
		md, err := schema.ParseMetadata(c.Metadata)
		if err != nil {
			return fmt.Errorf("the contract layer's %w", err)
		}
		if err := describes(md, p.want); err != nil {
			return fmt.Errorf("the contract layer's %w", err)
		}
		return nil
	})
	if err != nil {
		return Contract{}, fmt.Errorf("pull %s: %w", p.ref, err)
	}
	p.checked = true
	return c, nil
}
