package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/pki"
)

// nodeRenew issues a node a new node certificate from the node CA and delivers it. The node is
// reached even when its certificate expired: its chain is verified without dates, which only
// this command does. The node still verifies chalkctl's certificate as usual.
func (a *app) nodeRenew(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("node renew", flag.ContinueOnError)
	var n nodeCommand
	n.register(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: chalkctl node renew <node>")
	}
	t, err := a.target(ctx, n, pos[0])
	if err != nil {
		return err
	}
	conn, err := dialNode(t, true)
	if err != nil {
		return err
	}
	cert, err := pki.IssueNode(t.secrets.NodeCA, nodeNames(t), time.Now())
	if err != nil {
		return err
	}
	if _, err := conn.ApplyIdentity(ctx, connect.NewRequest(&nodev1.ApplyIdentityRequest{
		NodeCertificate: []byte(cert.Certificate),
		NodeKey:         []byte(cert.Key),
	})); err != nil {
		return fmt.Errorf("deliver the node certificate of %s: %w", t.name, nodeError(err, t.name))
	}
	leaf, err := pki.ParseCertificate([]byte(cert.Certificate))
	if err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "%s serves a new node certificate; it expires %s\n", t.name, leaf.NotAfter.UTC().Format(time.RFC3339))
	return nil
}
