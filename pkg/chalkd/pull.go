package chalkd

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// pullParts is the stream of the store, hash tree and UKI of the image h describes, which an
// upgrade pulls from ref. chalkd authenticates with the Registry of ref's host among the documents
// of the config text delivers, or without config of the running config, and pulls anonymously
// without one. Nothing is fetched until the stream is read.
func (s *Server) pullParts(ctx context.Context, ref string, h upgrade.Header, text string) (*ociimage.Pull, error) {
	docs, err := s.registryDocuments(text)
	if err != nil {
		return nil, fmt.Errorf("pull %s: %w", ref, err)
	}
	auth, err := registryAuth(ref, docs)
	if err != nil {
		return nil, err
	}
	want := ociimage.Header{
		ImageID: h.ImageID, RootHash: hex.EncodeToString(h.RootHash), Architecture: h.Architecture, Platform: h.Platform,
		Revision: h.Provenance.Revision, Tag: h.Provenance.Tag,
	}
	return ociimage.NewPull(ctx, ref, auth, want, []ociimage.Part{
		{MediaType: ociimage.StoreType, Size: h.StoreSize, SHA256: h.StoreSHA256},
		{MediaType: ociimage.HashTreeType, Size: h.VeritySize, SHA256: h.VeritySHA256},
		{MediaType: ociimage.UKIType, Size: h.UKISize, SHA256: h.UKISHA256},
	})
}

// registryDocuments are the documents whose Registry documents authenticate an upgrade's pull: the
// config text delivers, or without one the running config as it was delivered.
func (s *Server) registryDocuments(text string) ([]config.Document, error) {
	if text != "" {
		docs, err := config.Parse(strings.NewReader(text))
		if err != nil {
			return nil, fmt.Errorf("parse the config: %w", err)
		}
		return docs, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, running, _, err := s.runningSlot()
	if err != nil {
		return nil, plain(err)
	}
	return running.Delivered, nil
}

// registryAuth authenticates a pull of ref, an image by digest, with the Registry among docs whose
// host is ref's registry, as go-containerregistry names registries, or anonymously without one.
// The credentials go to that host alone: go-containerregistry sends no Authorization header to a
// host the registry redirects to.
func registryAuth(ref string, docs []config.Document) (authn.Authenticator, error) {
	r, err := name.NewDigest(ref, name.StrictValidation)
	if err != nil {
		return nil, fmt.Errorf("%q is no image reference by digest, repository@sha256:…: %w", ref, err)
	}
	creds, err := config.RegistryCredentialsOf(docs)
	if err != nil {
		return nil, fmt.Errorf("pull %s: the Registry documents: %w", ref, err)
	}
	for _, c := range creds {
		reg, err := name.NewRegistry(c.Host, name.StrictValidation)
		if err != nil {
			return nil, fmt.Errorf("pull %s: the Registry of %s: %w", ref, c.Host, err)
		}
		if reg.RegistryStr() != r.Context().RegistryStr() {
			continue
		}
		if c.Username == "" {
			return authn.Anonymous, nil
		}
		return &authn.Basic{Username: c.Username, Password: c.Password}, nil
	}
	return authn.Anonymous, nil
}
