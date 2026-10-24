package chalkd

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// pullParts is the stream of the store, hash tree and UKI of the image h describes, which an
// upgrade pulls from ref, as newPull pulls it through the Mirror and with the Registry documents of
// the config text delivers, or without config of the running config. A failure to read the running config is a
// connect error of its own code.
func (s *Server) pullParts(ctx context.Context, ref string, h upgrade.Header, text string) (*ociimage.Pull, error) {
	docs, err := s.registryDocuments(ref, text)
	if err != nil {
		return nil, err
	}
	return newPull(ctx, ref, h, docs)
}

// newPull is the stream of the store, hash tree, UKI and, when h names one, boot loader of the
// image h describes, pulled from ref through the Mirror of ref's registry and with the Registry
// documents among docs, as pullSources gives them. Nothing is fetched until the stream or its
// contract is read.
func newPull(ctx context.Context, ref string, h upgrade.Header, docs []config.Document) (*ociimage.Pull, error) {
	sources, err := pullSources(ref, docs)
	if err != nil {
		return nil, err
	}
	want := ociimage.Header{
		ImageID: h.ImageID, RootHash: hex.EncodeToString(h.RootHash), Architecture: h.Architecture, Platform: h.Platform,
		Revision: h.Provenance.Revision, Tag: h.Provenance.Tag,
	}
	parts := []ociimage.Part{
		{MediaType: ociimage.StoreType, Size: h.StoreSize, SHA256: h.StoreSHA256},
		{MediaType: ociimage.HashTreeType, Size: h.VeritySize, SHA256: h.VeritySHA256},
		{MediaType: ociimage.UKIType, Size: h.UKISize, SHA256: h.UKISHA256},
	}
	if h.BootLoaderSize > 0 {
		parts = append(parts, ociimage.Part{MediaType: ociimage.BootLoaderType, Size: h.BootLoaderSize, SHA256: h.BootLoaderSHA256})
	}
	return ociimage.NewPull(ctx, ref, sources, want, parts)
}

// registryDocuments are the documents whose Mirror and Registry documents direct the pull of ref: the
// config text delivers, or without one the running config as it was delivered.
func (s *Server) registryDocuments(ref, text string) ([]config.Document, error) {
	if text != "" {
		docs, err := config.Parse(strings.NewReader(text))
		if err != nil {
			return nil, fmt.Errorf("pull %s: parse the config: %w", ref, err)
		}
		return docs, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, running, _, err := s.runningSlot()
	if err != nil {
		return nil, failed(connect.CodeOf(err), "pull %s: %v", ref, plain(err))
	}
	return running.Delivered, nil
}

// pullSources are where a pull of ref goes, in order: each endpoint of the Mirror of ref's
// registry among docs, then the registry itself. Each authenticates with the Registry of its own
// host among docs, by any name of the host, or anonymously without one, and is reached over
// plain HTTP only if that Registry allows it. The credentials of one host never go to another,
// so an endpoint does not receive those of the registry it mirrors. They go to that host and the
// token service it names alone: go-containerregistry sends no Authorization header to a host the
// registry redirects to.
func pullSources(ref string, docs []config.Document) ([]ociimage.Source, error) {
	r, err := name.NewDigest(ref, name.StrictValidation)
	if err != nil {
		return nil, fmt.Errorf("%q is no image reference by digest, repository@sha256:…: %w", ref, err)
	}
	regs, err := config.RegistriesOf(docs, config.Documents(docs).Secrets().Lookup)
	if err != nil {
		return nil, fmt.Errorf("pull %s: the Registry and Mirror documents: %w", ref, err)
	}
	source := func(endpoint, host string) ociimage.Source {
		l, _ := regs.Login(host)
		s := ociimage.Source{Endpoint: endpoint, Auth: authn.Anonymous, Insecure: l.Insecure}
		if l.Username != "" {
			s.Auth = &authn.Basic{Username: l.Username, Password: l.Password}
		}
		return s
	}
	var out []ociimage.Source
	for _, e := range regs.Endpoints(r.Context().RegistryStr()) {
		// RegistriesOf checked the endpoint.
		u, _ := url.Parse(e)
		out = append(out, source(e, u.Host))
	}
	return append(out, source("", r.Context().RegistryStr())), nil
}
