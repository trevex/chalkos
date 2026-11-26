package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/config"
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
	// Documents are the node's config documents, as NodeDocuments leaves them. Secret documents
	// carry their values and no sources: the SDK resolves no sources.
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
}

// NewInstallHeader returns the header of an install of the request: the checked config, the
// node's certificate, the OS CA bundle, the fallback secret and the layers' payloads. d is what
// Check returned for r.Documents. The caller sets the target.
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

// Install installs in place the node in maintenance mode that conn reaches, with the image it runs.
// A config the image refuses is a *check.Refusal. An install that failed continues when it is run
// again.
func Install(ctx context.Context, conn *client.Conn, r InstallRequest) error {
	c, err := NodeContract(ctx, conn, r.Layers, r.Node)
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
