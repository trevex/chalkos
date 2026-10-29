package chalkd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/trevex/chalkos/pkg/chalkd/renewal"
	"github.com/trevex/chalkos/pkg/pki"
)

// NewNodeRenewal renews the node certificate once two thirds of its lifetime have passed, with a
// certificate issue obtains for a new key.
func NewNodeRenewal(cert *NodeCertificate, issue func(context.Context, []byte) (string, error)) *renewal.Renewal {
	due := func(now time.Time) (bool, time.Time, error) {
		leaf := cert.Current().Leaf
		return !now.Before(pki.RenewAt(leaf)), leaf.NotAfter, nil
	}
	return renewal.New("node certificate", due, func(ctx context.Context, now time.Time) error {
		return renewNodeCertificate(ctx, cert, issue, now)
	})
}

// renewNodeCertificate obtains a node certificate for a new key, checks it is for exactly the
// current certificate's names and lasts longer, and switches to it.
func renewNodeCertificate(ctx context.Context, cert *NodeCertificate, issue func(context.Context, []byte) (string, error), now time.Time) error {
	current := cert.Current().Leaf
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	request, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: current.Subject.CommonName}}, key)
	if err != nil {
		return err
	}
	chain, err := issue(ctx, request)
	if err != nil {
		return err
	}
	leaf, err := pki.ParseCertificate([]byte(chain))
	if err != nil {
		return fmt.Errorf("the issued certificate: %w", err)
	}
	if !pki.NamesOf(leaf).Equal(pki.NamesOf(current)) {
		return errors.New("the issued certificate is for other names than the node's")
	}
	if !leaf.NotAfter.After(current.NotAfter) {
		return fmt.Errorf("the issued certificate expires %s, no later than the current one", leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	keyPEM, err := pki.EncodeKey(key)
	if err != nil {
		return err
	}
	// ReplaceFrom checks that the key is the new one and that the chain leads to the OS CA.
	replaced, err := cert.ReplaceFrom(current, chain, keyPEM, now)
	if err != nil {
		return err
	}
	if !replaced {
		log.Print("node certificate: another one was delivered while the renewal ran; keeping it")
	}
	return nil
}
