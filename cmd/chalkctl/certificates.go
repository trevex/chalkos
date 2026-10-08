package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/manifest"
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

// nodeCARotate issues a new node CA from the OS CA, writes the secrets with it to a new file and
// delivers it to every control-plane node. Node certificates of the old node CA stay valid until
// they expire: they chain to the same OS CA.
func (a *app) nodeCARotate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("node-ca rotate", flag.ContinueOnError)
	var n nodeCommand
	n.register(fs)
	var recipients stringList
	fs.Var(&recipients, "recipient", "age recipient to encrypt the new secrets file to; may be repeated")
	plaintext := fs.Bool("plaintext", false, "write the new secrets file unencrypted")
	out := fs.String("out", "", "file to write the secrets with the new node CA to; it must not exist")
	publicOut := fs.String("public-out", "", "also write the public half, like secrets.pub.json, to this file")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return errors.New("usage: chalkctl node-ca rotate --out FILE (--recipient R... | --plaintext)")
	}
	if *out == "" {
		return errors.New("node-ca rotate: --out is required; the secrets file itself is never rewritten")
	}
	if (len(recipients) == 0) == !*plaintext {
		return errors.New("node-ca rotate: pass --recipient (one or more) or --plaintext")
	}
	c, err := a.loadCluster(ctx, n.cluster)
	if err != nil {
		return err
	}
	var controlPlanes []string
	for _, name := range slices.Sorted(maps.Keys(c.manifest.Nodes)) {
		if c.manifest.Roles[c.manifest.Nodes[name].Role].Kind == manifest.KindControlPlane {
			controlPlanes = append(controlPlanes, name)
		}
	}
	if n.endpoint != "" && len(controlPlanes) != 1 {
		return errors.New("node-ca rotate: --endpoint names one node's chalkd, but the cluster has several control-plane nodes")
	}
	secrets, err := a.loadSecrets(ctx, n.secrets, n.cluster.flake)
	if err != nil {
		return err
	}
	if secrets.NodeCA, err = pki.NewNodeCA(secrets.OSCA, time.Now()); err != nil {
		return err
	}
	if err := secrets.Validate(); err != nil {
		return err
	}
	if err := a.writeSecrets(secrets, recipients, *plaintext, *out, *publicOut); err != nil {
		return err
	}
	nodeCA, err := pki.ParseCertificate([]byte(secrets.NodeCA.Certificate))
	if err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "wrote %s with a new node CA, which expires %s; replace the secrets file with it\n", *out, nodeCA.NotAfter.UTC().Format(time.RFC3339))

	share, err := kpki.ControlPlaneShare(&secrets.Kubernetes, secrets.NodeCA).Encode()
	if err != nil {
		return err
	}
	var failed []string
	for _, name := range controlPlanes {
		err := a.deliverNodeCA(ctx, c, n.endpoint, name, secrets, share)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		fmt.Fprintf(a.stdout, "%s renews node certificates with the new node CA\n", name)
	}
	if len(failed) > 0 {
		return fmt.Errorf("the new node CA did not reach every control-plane node; deliver it with chalkctl apply-identity <node> --kubernetes-share --secrets %s: %s", *out, strings.Join(failed, "; "))
	}
	return nil
}

// deliverNodeCA delivers the control-plane share with the new node CA to a node, and nothing else.
func (a *app) deliverNodeCA(ctx context.Context, c *cluster, endpointFlag, name string, secrets pki.Secrets, share []byte) error {
	node, err := c.node(name)
	if err != nil {
		return err
	}
	addr, err := endpoint(endpointFlag, name, node.Identity)
	if err != nil {
		return err
	}
	conn, err := dialInstalled(&target{cluster: c, name: name, node: node, secrets: secrets, addr: addr})
	if err != nil {
		return err
	}
	_, err = conn.ApplyIdentity(ctx, connect.NewRequest(&nodev1.ApplyIdentityRequest{KubernetesShare: share}))
	return nodeError(err, name)
}
