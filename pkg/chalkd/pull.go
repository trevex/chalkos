package chalkd

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/upgrade"
)

// pullParts is the stream of the store, hash tree and UKI of the image h describes, which an
// upgrade pulls from ref, as newPull pulls it with the Registry documents of the config text
// delivers, or without config of the running config. A failure to read the running config is a
// connect error of its own code.
func (s *Server) pullParts(ctx context.Context, ref string, h upgrade.Header, text string) (*ociimage.Pull, error) {
	docs, err := s.registryDocuments(ref, text)
	if err != nil {
		return nil, err
	}
	return newPull(ctx, ref, h, docs)
}

// newPull is the stream of the store, hash tree, UKI and, when h names one, boot loader of the
// image h describes, pulled from ref with the credentials of the Registry of ref's host among
// docs, or anonymously without one. It pulls over HTTPS alone unless that Registry allows plain
// HTTP. Nothing is fetched until the stream or its contract is read.
func newPull(ctx context.Context, ref string, h upgrade.Header, docs []config.Document) (*ociimage.Pull, error) {
	auth, insecure, err := registryAuth(ref, docs)
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
	return ociimage.NewPull(ctx, ref, []ociimage.Source{{Auth: auth, Insecure: insecure}}, want, parts)
}

// registryDocuments are the documents whose Registry documents authenticate the pull of ref: the
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

// registryAuth authenticates a pull of ref, an image by digest, with the Registry among docs whose
// host is ref's registry, as go-containerregistry names registries and in any case, or
// anonymously without one, and reports whether that Registry allows plain HTTP. The credentials
// go to that host and the token service it names alone: go-containerregistry sends no
// Authorization header to a host the registry redirects to, and without insecure every request
// is HTTPS.
func registryAuth(ref string, docs []config.Document) (authn.Authenticator, bool, error) {
	r, err := name.NewDigest(ref, name.StrictValidation)
	if err != nil {
		return nil, false, fmt.Errorf("%q is no image reference by digest, repository@sha256:…: %w", ref, err)
	}
	secrets, err := config.SecretsOf(docs)
	if err != nil {
		return nil, false, fmt.Errorf("pull %s: the Secrets: %w", ref, err)
	}
	regs, err := config.RegistriesOf(docs, secrets)
	if err != nil {
		return nil, false, fmt.Errorf("pull %s: the Registry and Mirror documents: %w", ref, err)
	}
	l, ok := regs.Login(r.Context().RegistryStr())
	switch {
	case !ok:
		return authn.Anonymous, false, nil
	case l.Username == "":
		return authn.Anonymous, l.Insecure, nil
	}
	return &authn.Basic{Username: l.Username, Password: l.Password}, l.Insecure, nil
}
