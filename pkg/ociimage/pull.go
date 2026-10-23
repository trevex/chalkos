package ociimage

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
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
// The image comes from the first of its sources that serves the manifest: mirror endpoints in
// order, then the registry itself. A source that cannot be reached, or answers 401, 403 or 404, is
// passed over, for the manifest and for each layer, which starts at the source of the manifest;
// other errors, such as bytes that do not match the digest, fail the pull.
//
// Contract fetches the manifest, the header and the contract layer up front, as an install needs
// them before it reads a part.
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

// Source is where a Pull fetches an image from, and how.
type Source struct {
	// Endpoint is the URL of a mirror's endpoint, http:// or https://, with the path the registry
	// API is below if it has one: the API is at <path>/v2, or at <path> when it ends in /v2, as
	// containerd's hosts.toml takes it. Empty is the registry the reference names.
	Endpoint string
	// Auth authenticates to the source; authn.Anonymous pulls without credentials.
	Auth authn.Authenticator
	// Insecure allows plain HTTP: to the registry the reference names, which is otherwise reached
	// over HTTPS alone, and to an http:// endpoint, which needs it.
	Insecure bool
}

// source is a Source ready to fetch from: the reference on its host, and its puller.
type source struct {
	name   string
	ref    name.Digest
	puller *remote.Puller
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

// newSource is s ready to fetch the image ref names: on an endpoint, the same repository and
// digest on the endpoint's host, below its path.
func newSource(ref name.Digest, s Source) (source, error) {
	host, prefix := ref.Context().RegistryStr(), ""
	https := !s.Insecure
	label := "the registry " + host
	if s.Endpoint != "" {
		u, err := url.Parse(s.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return source{}, fmt.Errorf("the mirror %q is no http:// or https:// URL", s.Endpoint)
		}
		if u.Scheme == "http" && !s.Insecure {
			return source{}, fmt.Errorf("the mirror %s is plain HTTP; set insecure: true in the Registry of %s to allow it", s.Endpoint, u.Host)
		}
		host, https, label = u.Host, u.Scheme == "https", "the mirror "+s.Endpoint
		if p := path.Clean("/" + u.Path); p != "/" {
			prefix = strings.TrimSuffix(p, "/v2")
		}
	}
	opts := []name.Option{name.StrictValidation}
	if !https {
		opts = append(opts, name.Insecure)
	}
	d, err := name.NewDigest(host+"/"+ref.Context().RepositoryStr()+"@"+ref.DigestStr(), opts...)
	if err != nil {
		return source{}, fmt.Errorf("%s: %w", label, err)
	}
	var t http.RoundTripper = remote.DefaultTransport
	if prefix != "" {
		t = prefixed{next: t, host: host, prefix: prefix}
	}
	if https {
		t = httpsOnly{next: t}
	}
	pl, err := remote.NewPuller(remote.WithAuth(s.Auth), remote.WithTransport(t))
	if err != nil {
		return source{}, err
	}
	return source{name: label, ref: d, puller: pl}, nil
}

// prefixed puts prefix before the path of each request to host's registry API, below /v2, as
// containerd reaches an endpoint with a path. Other requests, such as to a token service, pass
// as they are.
type prefixed struct {
	next         http.RoundTripper
	host, prefix string
}

func (t prefixed) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.EqualFold(r.URL.Host, t.host) && (r.URL.Path == "/v2" || strings.HasPrefix(r.URL.Path, "/v2/")) {
		r = r.Clone(r.Context())
		r.URL.Path, r.URL.RawPath = t.prefix+r.URL.Path, ""
	}
	return t.next.RoundTrip(r)
}

// passable reports whether err says a source cannot serve the image, so a fetch tries the next
// source: it could not be reached, or it answered 401, 403 or 404.
func passable(err error) bool {
	var te *transport.Error
	if errors.As(err, &te) {
		return te.StatusCode == http.StatusUnauthorized || te.StatusCode == http.StatusForbidden || te.StatusCode == http.StatusNotFound
	}
	var op *net.OpError
	var dns *net.DNSError
	var cert *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	return errors.As(err, &op) || errors.As(err, &dns) || errors.As(err, &cert) || errors.As(err, &record)
}

// after is err, the error of the source a fetch ended at, naming the sources passed over before
// it and why.
func after(err error, passed []string) error {
	if len(passed) == 0 {
		return err
	}
	return fmt.Errorf("%w, after %s", err, strings.Join(passed, "; "))
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
	var passed []string
	for i := p.src; i < len(p.sources); i++ {
		s := p.sources[i]
		ctx, cancel := context.WithCancelCause(p.ctx)
		stalled := fmt.Errorf("no data from the registry for %v while pulling the %s layer of %s", stallTimeout, part.MediaType, p.ref)
		stall := time.AfterFunc(stallTimeout, func() { cancel(stalled) })
		l, err := s.puller.Layer(ctx, s.ref.Context().Digest("sha256:"+hex.EncodeToString(part.SHA256)))
		var rc io.ReadCloser
		if err == nil {
			rc, err = l.Compressed()
		}
		stall.Stop()
		if err == nil {
			p.cur, p.left, p.cancel, p.stall, p.ctxCur, p.stalled = rc, part.Size, cancel, stall, ctx, stalled
			return nil
		}
		if context.Cause(ctx) == stalled {
			err = stalled
		}
		cancel(nil)
		if i < len(p.sources)-1 && p.ctx.Err() == nil && (err == stalled || passable(err)) {
			passed = append(passed, s.name+": "+err.Error())
			continue
		}
		if err == stalled {
			return after(stalled, passed)
		}
		return fmt.Errorf("pull %s: the %s layer: %w", p.ref, part.MediaType, after(err, passed))
	}
	return fmt.Errorf("pull %s: no source to pull from", p.ref)
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
	desc, err := p.manifest(ctx)
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

// manifest fetches the manifest from the first source that serves it, which the layers then
// start at.
func (p *Pull) manifest(ctx context.Context) (*remote.Descriptor, error) {
	var passed []string
	for i, s := range p.sources {
		desc, err := s.puller.Get(ctx, s.ref)
		if err == nil {
			p.src = i
			return desc, nil
		}
		if i == len(p.sources)-1 || ctx.Err() != nil || !passable(err) {
			return nil, after(err, passed)
		}
		passed = append(passed, s.name+": "+err.Error())
	}
	return nil, errors.New("no source to pull from")
}

// Contract fetches the manifest and the header, refusing them as the first read does, and reads
// the image's contract layer, refusing one whose metadata.json describes another image than the
// header. Reads after it succeeded fetch the manifest no more.
func (p *Pull) Contract() (Contract, error) {
	ctx, cancel := context.WithTimeout(p.ctx, manifestTimeout)
	defer cancel()
	img, err := p.image(ctx)
	if err != nil {
		return Contract{}, fmt.Errorf("pull %s: %w", p.ref, err)
	}
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
	p.checked = true
	return c, nil
}
