package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/storage"
)

// NodeNames are the names a node's certificate carries: its name, the hostname its Host document
// gives and its static addresses.
func NodeNames(node string, docs []config.Document) pki.NodeNames {
	hostnames := []string{node}
	if h := config.Hostname(docs); h != "" && h != node {
		hostnames = append(hostnames, h)
	}
	var ips []net.IP
	for _, a := range config.StaticAddresses(docs) {
		if ip := net.ParseIP(a); ip != nil {
			ips = append(ips, ip)
		}
	}
	return pki.NodeNames{CommonName: node, DNSNames: hostnames, IPs: ips}
}

// InstallRequest is what installing a node in maintenance mode takes.
type InstallRequest struct {
	// Node is the node's name, which its node certificate carries.
	Node string
	// Documents are the node's config documents. Install keeps those that NodeDocuments leaves
	// the node, so documents a caller filtered already stay the same. Secret documents carry
	// their values and no sources: the SDK resolves no sources.
	Documents []config.Document
	// Secrets are the cluster's secrets, which issue the node's certificate, its recovery key and
	// the layers' payloads.
	Secrets clustersecrets.Secrets
	// Layers are the layers the client knows, which check the config and issue payloads.
	Layers []Layer
	// Password is the fallback secret of a config whose storage falls back to a password.
	Password string
	// Now is when the node's certificate and the layers' payloads are issued; the zero time is
	// time.Now().
	Now time.Time
	// Image, when set, is the image the node installs in place of the one it runs: its reference
	// in a registry by digest, repository@sha256:…. Install reads the image's header and contract
	// layer with the credentials Keychain resolves, refuses with ErrOSCARefused an image that carries
	// an OS CA other than the one that issues the secrets' client certificates, as CheckImageOSCA
	// does, and checks the config against that contract. An image built before a rotation of the OS
	// CA finished must be built again with the current OS CA.
	// The node pulls the image with the credentials of the Registry of the reference's
	// host among Documents, writes the image into its inactive slot, has the image's own checker
	// check the config there and boots the image once it is installed. The node keeps the disk
	// layout of the image it booted from, and its chalkd translates the config with the image's
	// schemas only to plan the layers' payloads: the image's checker decides. Empty installs the
	// image the node runs.
	//
	// Two failures are left to the caller. When the image's first boot is never found healthy, the
	// node falls back to the image it booted from, installed but without a config slot for that
	// image; RollbackImage, an Upgrade that carries config or a new machine recovers it. A power
	// loss after the image is activated and before the node is marked installed boots the image
	// in maintenance mode, where the same Install repeated finishes the install.
	Image string
	// Keychain resolves the credentials Install reads Image with; nil means authn.DefaultKeychain.
	// A Cluster API provider passes the pull secrets of the machine's image here.
	Keychain authn.Keychain
}

// NewInstallHeader returns the header of an install of the request: the checked config, the
// node's certificate, the OS CA bundle, the fallback secret and the layers' payloads. d is what
// Check returned for r.Documents. The caller sets the target and the image; NewInstallHeader
// ignores r.Image and r.Keychain, and takes r.Documents as they are, without NodeDocuments.
func NewInstallHeader(r InstallRequest, d Delivery) (*nodev1.InstallHeader, error) {
	now := r.Now
	if now.IsZero() {
		now = time.Now()
	}
	cert, err := pki.IssueNode(r.Secrets.NodeCA, NodeNames(r.Node, r.Documents), now)
	if err != nil {
		return nil, err
	}
	fallback, err := fallbackSecret(r, d.Identity.Storage.Fallback)
	if err != nil {
		return nil, err
	}
	payloads, err := PayloadsOf(r.Layers, r.Secrets, r.Node, r.Documents, now)
	if err != nil {
		return nil, err
	}
	return &nodev1.InstallHeader{
		Config:          d.Text,
		CaCertificate:   []byte(r.Secrets.OSCABundle()),
		NodeCertificate: []byte(cert.Certificate),
		NodeKey:         []byte(cert.Key),
		FallbackSecret:  fallback,
		Layers:          payloads,
	}, nil
}

// fallbackSecret is the secret enrolled as the second keyslot of the node's encrypted volumes for
// the storage fallback: its derived recovery key, or the request's password.
func fallbackSecret(r InstallRequest, fallback string) (string, error) {
	switch fallback {
	case storage.FallbackNone:
		return "", nil
	case storage.FallbackRecoveryKey:
		return clustersecrets.RecoveryKey(r.Secrets.RecoverySecret, r.Node)
	case storage.FallbackPassword:
		if r.Password == "" {
			return "", fmt.Errorf("node %s unlocks with a password when its TPM fails, but the install request has none", r.Node)
		}
		return r.Password, nil
	}
	return "", fmt.Errorf("node %s has the unknown fallback %q", r.Node, fallback)
}

// Install installs in place the node in maintenance mode that conn reaches, with the image it runs
// or with r.Image. A config the image refuses, as Install checks it first or as the node checks
// it, is Refused. An install that failed continues when it is run again.
func Install(ctx context.Context, conn *client.Conn, r InstallRequest) error {
	r.Documents = NodeDocuments(r.Layers, r.Documents)
	var (
		c   Contract
		img *nodev1.ImageHeader
		ref string
		err error
	)
	if r.Image == "" {
		c, err = NodeContract(ctx, conn, r.Layers, r.Node)
	} else {
		c, img, ref, err = registryImage(ctx, r)
	}
	if err != nil {
		return err
	}
	d, err := Check(ctx, c, r.Node, r.Documents)
	if err != nil {
		return fmt.Errorf("the config of %s: %w", r.Node, err)
	}
	header, err := NewInstallHeader(r, d)
	if err != nil {
		return err
	}
	header.Target = &nodev1.InstallHeader_InPlace{InPlace: &nodev1.InPlace{}}
	if img != nil {
		header.Image = img
		header.Origin = &nodev1.InstallHeader_Source{Source: &nodev1.ImageSource{Source: &nodev1.ImageSource_Oci{Oci: &nodev1.RegistryImage{Reference: ref}}}}
	}
	stream, err := conn.Install(ctx)
	if err != nil {
		return err
	}
	if err := stream.Send(&nodev1.InstallRequest{Message: &nodev1.InstallRequest_Header{Header: header}}); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if _, err := stream.CloseAndReceive(); err != nil {
		return fmt.Errorf("install %s: %w", r.Node, err)
	}
	return nil
}

// registryImage reads the image in a registry r.Image names, by digest: its contract, the header
// of an install in place of it, without the boot loader the node keeps, and its reference. It
// refuses an image whose OS CA the secrets do not trust.
func registryImage(ctx context.Context, r InstallRequest) (Contract, *nodev1.ImageHeader, string, error) {
	if _, err := name.NewDigest(r.Image, name.StrictValidation); err != nil {
		return Contract{}, nil, "", fmt.Errorf("install %s: the image %q is no reference by digest, repository@sha256:…: %w", r.Node, r.Image, err)
	}
	kc := r.Keychain
	if kc == nil {
		kc = authn.DefaultKeychain
	}
	a, err := ociimage.Read(ctx, r.Image, kc)
	if err != nil {
		return Contract{}, nil, "", fmt.Errorf("install %s: %w", r.Node, err)
	}
	if err := CheckImageOSCA(a.Metadata, r.Secrets); err != nil {
		return Contract{}, nil, "", fmt.Errorf("install %s with %s: %w", r.Node, a.Reference, err)
	}
	c, err := ArtifactContract(r.Layers, a)
	if err != nil {
		return Contract{}, nil, "", fmt.Errorf("install %s: %w", r.Node, err)
	}
	h, err := RegistryImageHeader(a, false)
	if err != nil {
		return Contract{}, nil, "", fmt.Errorf("install %s: %w", r.Node, err)
	}
	return c, h, a.Reference, nil
}
