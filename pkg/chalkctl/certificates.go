package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/pki"
)

// nodeRenew issues a node a new node certificate from the node CA and delivers it. The node is
// reached even when its certificate expired: its chain is verified without dates, which only
// this command does. The node still verifies chalkctl's certificate as usual.
func (a *app) nodeRenewCommand() *cobra.Command {
	var n nodeCommand
	cmd := a.command(&cobra.Command{
		Use:     "renew <node>",
		Short:   "Issue a node a new node certificate, also once its own expired",
		Long:    nodeRenewLong,
		Example: nodeRenewExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.nodeRenew(ctx, n, pos) })
	n.register(cmd.Flags())
	return cmd
}

func (a *app) nodeRenew(ctx context.Context, n nodeCommand, pos []string) error {
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

// nodeCARotate issues a new node CA from the OS CA, writes the secrets file with it and delivers
// it to every control-plane node. Node certificates of the old node CA stay valid until they
// expire: they chain to the same OS CA.
func (a *app) nodeCARotateCommand() *cobra.Command {
	var n nodeCommand
	var change changeFlags
	cmd := a.command(&cobra.Command{
		Use:     "rotate",
		Short:   "Issue a new node CA and deliver it to the control planes",
		Long:    nodeCARotateLong,
		Example: nodeCARotateExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.nodeCARotate(ctx, n, change, pos) })
	n.register(cmd.Flags())
	change.register(cmd.Flags())
	return cmd
}

func (a *app) nodeCARotate(ctx context.Context, n nodeCommand, change changeFlags, pos []string) error {
	if len(pos) != 0 {
		return errors.New("usage: chalkctl node-ca rotate [--out FILE] [--public-out FILE] [--recipient R...]")
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
	// A node CA no control plane holds renews nothing.
	if len(controlPlanes) == 0 {
		return errors.New("node-ca rotate: the cluster has no control-plane node to renew node certificates with the new node CA")
	}
	if n.endpoint != "" && len(controlPlanes) != 1 {
		return errors.New("node-ca rotate: --endpoint names one node's chalkd, but the cluster has several control-plane nodes")
	}
	f, err := a.openSecrets(ctx, n.secrets, n.cluster.flake, change)
	if err != nil {
		return err
	}
	defer f.Close()
	secrets := f.secrets
	// A rotation delivers its own shares, one control plane at a time.
	if r := secrets.Rotation; r != nil {
		return fmt.Errorf("node-ca rotate: %v", &pki.ErrRotationRuns{Kind: r.Kind, Phase: r.Phase})
	}
	if secrets.NodeCA, err = pki.NewNodeCA(secrets.OSCA, time.Now()); err != nil {
		return err
	}
	if err := a.writeSecretsFile(f, secrets); err != nil {
		return err
	}
	nodeCA, err := pki.ParseCertificate([]byte(secrets.NodeCA.Certificate))
	if err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "%s holds a new node CA, which expires %s; %s\n", f.out, nodeCA.NotAfter.UTC().Format(time.RFC3339), f.written())

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
		return fmt.Errorf("the new node CA did not reach every control-plane node; deliver it with chalkctl apply-identity <node> --kubernetes-share --secrets %s: %s", f.out, strings.Join(failed, "; "))
	}
	return nil
}

// deliverNodeCA delivers the control-plane share with the new node CA to a node, and nothing else.
func (a *app) deliverNodeCA(ctx context.Context, c *cluster, endpointFlag, name string, secrets pki.Secrets, share []byte) error {
	node, err := c.node(name)
	if err != nil {
		return err
	}
	addr, source, err := c.endpoint(endpointFlag, nil, name, node.Identity)
	if err != nil {
		return err
	}
	creds, err := secretCredentials(secrets)
	if err != nil {
		return err
	}
	conn, err := dialInstalled(&target{cluster: c, name: name, node: node, creds: creds, secrets: secrets, addr: addr, source: source})
	if err != nil {
		return err
	}
	_, err = conn.ApplyIdentity(ctx, connect.NewRequest(&nodev1.ApplyIdentityRequest{KubernetesShare: share}))
	return nodeError(err, name)
}
