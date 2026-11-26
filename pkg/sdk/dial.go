package sdk

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/pki"
)

// ErrInstalled is the error of DialMaintenance at a node that runs in normal mode: it is
// installed already.
var ErrInstalled = errors.New("the node is installed already")

// ErrOSCARefused is the error of a node in maintenance mode whose image carries an OS CA that did
// not issue the client certificate.
var ErrOSCARefused = errors.New("the node's image trusts another OS CA than the client certificate's")

// Trust says how a connection verifies the node. It must name exactly one of Fingerprint,
// Insecure and OSCAs; IgnoreValidity and Name apply only together with OSCAs.
type Trust struct {
	// Fingerprint pins the node's certificate, as a node in maintenance mode presents it.
	Fingerprint string
	// Insecure accepts any certificate; Conn.Fingerprint reports the one the node presented.
	Insecure bool
	// OSCAs verify an installed node's certificate.
	OSCAs *x509.CertPool
	// Name is the node name the certificate must be valid for when OSCAs verify it.
	Name string
	// IgnoreValidity accepts an expired node certificate, as a renewal needs.
	IgnoreValidity bool
	// Source says where the address came from, such as --address, for errors.
	Source string
	// Untrusted, when set, replaces the error of a call whose TLS handshake failed for want of
	// trust in either direction, as client.Options.Untrusted does.
	Untrusted func(err error, refused bool) error
}

// verifier builds the Verifier t names, after refusing an ambiguous trust: exactly one of
// Fingerprint, Insecure and OSCAs, and IgnoreValidity or Name only together with OSCAs.
func (t Trust) verifier() (client.Verifier, error) {
	named := 0
	for _, is := range [...]bool{t.Fingerprint != "", t.Insecure, t.OSCAs != nil} {
		if is {
			named++
		}
	}
	switch {
	case named == 0:
		return nil, errors.New("the trust names no fingerprint, OS CAs or insecure")
	case named > 1:
		return nil, errors.New("the trust names more than one of a fingerprint, OS CAs and insecure")
	case t.OSCAs == nil && (t.IgnoreValidity || t.Name != ""):
		return nil, errors.New("the trust names IgnoreValidity or a name without OS CAs")
	}
	switch {
	case t.Fingerprint != "":
		return client.ByFingerprint(t.Fingerprint), nil
	case t.Insecure:
		return client.AnyCertificate(), nil
	case t.IgnoreValidity:
		return client.ByCAIgnoringValidity(t.OSCAs, t.Name), nil
	default:
		return client.ByCA(t.OSCAs, t.Name), nil
	}
}

// Dial connects to the node at addr, verified as t says, presenting cert; nil presents none.
func Dial(addr string, t Trust, cert *tls.Certificate) (*client.Conn, error) {
	v, err := t.verifier()
	if err != nil {
		return nil, err
	}
	return client.Dial(addr, client.Options{Verify: v, Certificate: cert, Source: t.Source, Untrusted: t.Untrusted})
}

// DialMaintenance connects to a node in maintenance mode at addr, presenting cert, verified by
// t.Fingerprint or t.Insecure (exactly one; t must name no OS CA or Untrusted, since ErrOSCARefused
// already explains a refusal there) and, for errors, t.Source. With t.Insecure it trusts the node
// on first use: it reads the fingerprint over an unverified connection and dials again pinned to
// it, so what follows goes to that certificate alone. It returns the connection, the node's Info
// and its certificate's fingerprint, which a caller such as a Cluster API provider records. A
// node in normal mode fails with ErrInstalled, with the connection closed but its Info and
// fingerprint still returned, so a caller can act on what the node answered, such as a warning of
// its release, before saying it is installed. A node whose image refuses cert fails with
// ErrOSCARefused and no Info.
func DialMaintenance(ctx context.Context, addr string, t Trust, cert *tls.Certificate) (*client.Conn, *nodev1.InfoResponse, string, error) {
	switch {
	case t.OSCAs != nil || t.Name != "" || t.IgnoreValidity:
		return nil, nil, "", errors.New("a node in maintenance mode is verified by its certificate's fingerprint or insecurely, never by an OS CA")
	case t.Untrusted != nil:
		return nil, nil, "", errors.New("a node in maintenance mode names no Untrusted; ErrOSCARefused explains refusals there")
	case t.Fingerprint != "" && t.Insecure:
		return nil, nil, "", errors.New("the trust names both a fingerprint and insecure")
	case t.Fingerprint == "" && !t.Insecure:
		return nil, nil, "", errors.New("the trust names neither a fingerprint nor insecure")
	}
	fingerprint := t.Fingerprint
	if t.Insecure {
		probe, err := Dial(addr, Trust{Insecure: true, Source: t.Source}, cert)
		if err != nil {
			return nil, nil, "", err
		}
		_, err = probe.Info(ctx, &nodev1.InfoRequest{})
		fingerprint = probe.Fingerprint()
		probe.Close()
		if err != nil {
			return nil, nil, "", maintenanceError(ctx, addr, cert, err)
		}
	}
	conn, err := Dial(addr, Trust{Fingerprint: fingerprint, Source: t.Source}, cert)
	if err != nil {
		return nil, nil, fingerprint, err
	}
	info, err := conn.Info(ctx, &nodev1.InfoRequest{})
	if err != nil {
		conn.Close()
		return nil, nil, fingerprint, maintenanceError(ctx, addr, cert, err)
	}
	if info.Mode != nodev1.Mode_MODE_MAINTENANCE {
		conn.Close()
		return nil, info, fingerprint, ErrInstalled
	}
	return conn, info, fingerprint, nil
}

// maintenanceError maps the error of a call to a node in maintenance mode to ErrOSCARefused when
// the node's image refused cert for an unknown issuer, and returns it as is otherwise: in
// particular, it never maps to ErrOSCARefused without a cert to have been refused.
func maintenanceError(ctx context.Context, addr string, cert *tls.Certificate, err error) error {
	if cert != nil && client.RefusesCertificate(ctx, err, addr, cert) {
		return fmt.Errorf("%w: %w", ErrOSCARefused, err)
	}
	return err
}

// ErrNotInstalled is the error of DialInstalled at a node that runs in maintenance mode: it is
// not installed yet, or the image it installed boots in maintenance mode until the install ends.
var ErrNotInstalled = errors.New("the node is not installed: it runs in maintenance mode")

// ErrOtherNode is the error of DialInstalled at a node installed under another name than the one
// dialled.
var ErrOtherNode = errors.New("the node is installed under another name")

// DialInstalled connects to the installed node called name at addr, verified by the OS CAs s
// trusts, those that issue and those accepted during a rotation, and presenting cert. It returns
// the connection and the node's Info, whose RootHash names the image the node runs. A node whose
// certificate the OS CAs issued for another node, or whose Info names another node, fails with
// ErrOtherNode. A node whose certificate the OS CAs did not issue fails with the client's
// verification error, unless it says over an unverified connection that it runs in maintenance
// mode: then the error wraps ErrNotInstalled and the verification error. That answer is not
// authenticated; a caller that acts on it continues with DialMaintenance, which pins the node's
// fingerprint. No error returns a connection or Info. A node that fell back to the image it
// booted from, because the first boot of an image it installed was never found healthy, answers
// in normal mode with the RootHash of that other image.
func DialInstalled(ctx context.Context, addr string, s clustersecrets.Secrets, name string, cert *tls.Certificate) (*client.Conn, *nodev1.InfoResponse, error) {
	pool, err := pki.BundlePool(s.OSCABundle())
	if err != nil {
		return nil, nil, fmt.Errorf("the secrets' OS CAs: %w", err)
	}
	conn, err := Dial(addr, Trust{OSCAs: pool, Name: name}, cert)
	if err != nil {
		return nil, nil, err
	}
	info, err := conn.Info(ctx, &nodev1.InfoRequest{})
	if err != nil {
		conn.Close()
		return nil, nil, installedError(ctx, addr, name, cert, err)
	}
	switch {
	case info.Mode != nodev1.Mode_MODE_NORMAL:
		conn.Close()
		return nil, nil, fmt.Errorf("%s: %w", addr, ErrNotInstalled)
	case info.NodeName != name:
		conn.Close()
		return nil, nil, fmt.Errorf("%w: the node at %s is %s, not %s", ErrOtherNode, addr, info.NodeName, name)
	}
	return conn, info, nil
}

// installedError maps the error of an Info call to a node DialInstalled verified by the OS CAs. A
// certificate not valid for name chained to the OS CAs, as the client checks the chain first, so
// the node is another node of the cluster: ErrOtherNode. When the OS CAs did not issue the
// certificate, it asks the node without verifying it, and a node that says it runs in maintenance
// mode is ErrNotInstalled. It returns err as is otherwise.
func installedError(ctx context.Context, addr, name string, cert *tls.Certificate, err error) error {
	if _, ok := errors.AsType[x509.HostnameError](err); ok {
		return fmt.Errorf("%w: the node at %s has no certificate for %s: %w", ErrOtherNode, addr, name, err)
	}
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); !ok {
		return err
	}
	probe, perr := Dial(addr, Trust{Insecure: true}, cert)
	if perr != nil {
		return err
	}
	defer probe.Close()
	if info, perr := probe.Info(ctx, &nodev1.InfoRequest{}); perr == nil && info.Mode == nodev1.Mode_MODE_MAINTENANCE {
		return fmt.Errorf("%s: %w: %w", addr, ErrNotInstalled, err)
	}
	return err
}
