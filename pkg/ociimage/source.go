package ociimage

import (
	"crypto/tls"
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
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// retryBackoff is how often, and after how long, a source's fetch is retried after a temporary
// error or a 429 or 5xx answer, before the source is passed over: go-containerregistry's default.
var retryBackoff = remote.Backoff{Duration: time.Second, Factor: 3, Jitter: 0.1, Steps: 3}

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
	t := remote.DefaultTransport
	if prefix != "" {
		t = prefixed{next: t, host: host, prefix: prefix}
	}
	if https {
		t = httpsOnly{next: t}
	}
	pl, err := remote.NewPuller(remote.WithAuth(s.Auth), remote.WithTransport(t), remote.WithRetryBackoff(retryBackoff))
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
// source: it could not be reached, the connection failed before a response, or it answered 401,
// 403, 404, 429 or a 5xx status, as containerd passes over a host.
func passable(err error) bool {
	var te *transport.Error
	if errors.As(err, &te) {
		c := te.StatusCode
		return c == http.StatusUnauthorized || c == http.StatusForbidden || c == http.StatusNotFound ||
			c == http.StatusTooManyRequests || c >= http.StatusInternalServerError
	}
	// The HTTP client wraps the errors of a request that got no response in a url.Error: a
	// connection closed before or during the TLS handshake, a timeout, or a plain HTTP answer to
	// HTTPS. A body cut short is no url.Error and fails the pull.
	var ue *url.Error
	if errors.As(err, &ue) && (ue.Timeout() || errors.Is(ue.Err, io.EOF) || errors.Is(ue.Err, io.ErrUnexpectedEOF)) {
		return true
	}
	var op *net.OpError
	var dns *net.DNSError
	var cert *tls.CertificateVerificationError
	var alert tls.AlertError
	return errors.As(err, &op) || errors.As(err, &dns) || errors.As(err, &cert) || errors.As(err, &alert) ||
		errors.Is(err, http.ErrSchemeMismatch)
}

// after is err, the error of the source a fetch ended at, naming the sources passed over before
// it and why.
func after(err error, passed []string) error {
	if len(passed) == 0 {
		return err
	}
	return fmt.Errorf("%w, after %s", err, strings.Join(passed, "; "))
}

// passedOver is how after names a source passed over: its name and its error, cut to 512 bytes,
// as a source may answer with any text.
func passedOver(s source, err error) string {
	msg := err.Error()
	if len(msg) > 512 {
		msg = strings.ToValidUTF8(msg[:512], "") + "…"
	}
	return s.name + ": " + msg
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
