package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/manifest"
)

// kubernetesShare issues the share of a node whose role has Kubernetes; nil for one without.
func kubernetesShare(t *target, now time.Time) ([]byte, error) {
	role, ok := t.cluster.manifest.Roles[t.node.Role]
	if !ok {
		return nil, fmt.Errorf("the cluster has no role %s", t.node.Role)
	}
	if role.Kind == "" {
		return nil, nil
	}
	share, err := kpki.ShareFor(&t.secrets, role.Kind, t.name, now)
	if err != nil {
		return nil, err
	}
	return share.Encode()
}

func (a *app) bootstrapCommand() *cobra.Command {
	var n nodeCommand
	var timeout time.Duration
	cmd := a.command(&cobra.Command{
		Use:     "bootstrap <node>",
		Short:   "Initialise the cluster on a control plane",
		Long:    bootstrapLong,
		Example: bootstrapExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.bootstrap(ctx, n, timeout, pos) })
	n.registerClient(cmd.Flags())
	cmd.Flags().DurationVar(&timeout, "timeout", 20*time.Minute, "how long to wait for the control plane to apply its manifests")
	return cmd
}

func (a *app) bootstrap(ctx context.Context, n nodeCommand, timeout time.Duration, pos []string) error {
	if len(pos) != 1 {
		return errors.New("usage: chalkctl bootstrap <node>")
	}
	t, err := a.clientTarget(ctx, n, pos[0])
	if err != nil {
		return err
	}
	// Without the cluster definition the node itself refuses when it is a worker.
	if kind := t.cluster.manifest.Roles[t.node.Role].Kind; !t.cluster.partial && kind != manifest.KindControlPlane {
		return fmt.Errorf("%s is not a control-plane node; bootstrap one of the cluster's control-plane nodes", t.name)
	}
	conn, err := dialInstalled(t)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fmt.Fprintf(a.stdout, "bootstrapping %s; the control plane pulls its images and starts\n", t.name)
	resp, err := conn.Bootstrap(ctx, connect.NewRequest(&nodev1.BootstrapRequest{}))
	if err != nil {
		return fmt.Errorf("bootstrap %s: %w", t.name, err)
	}
	fmt.Fprintf(a.stdout, "%s is bootstrapped; applied %s\n", t.name, count(resp.Msg.Applied, "object"))
	return nil
}

type kubeconfigFlags struct {
	cluster           clusterFlags
	secrets           secretFlags
	ttl               time.Duration
	out, server, name string
	force             bool
}

func (a *app) kubeconfigCommand() *cobra.Command {
	var f kubeconfigFlags
	cmd := a.command(&cobra.Command{
		Use:     "kubeconfig",
		Short:   "Write an admin kubeconfig",
		Long:    kubeconfigLong,
		Example: kubeconfigExample,
	}, func(a *app, ctx context.Context, pos []string) error { return a.kubeconfig(ctx, f, pos) })
	fs := cmd.Flags()
	f.cluster.register(fs)
	f.secrets.register(fs)
	fs.DurationVar(&f.ttl, "ttl", 8760*time.Hour, "validity of the admin certificate")
	fs.StringVar(&f.out, "out", "kubeconfig", "file to write, - for standard output")
	fs.BoolVar(&f.force, "force", false, "replace an existing file")
	fs.StringVar(&f.server, "server", "", "URL clients reach the API server at, when it differs from the cluster endpoint; the certificate is still verified for the endpoint")
	fs.StringVar(&f.name, "name", "admin", "user name in the admin certificate")
	return cmd
}

func (a *app) kubeconfig(ctx context.Context, f kubeconfigFlags, pos []string) error {
	cf, sf := f.cluster, f.secrets
	if len(pos) != 0 {
		return errors.New("usage: chalkctl kubeconfig [--ttl 8760h] [--out FILE] [--server URL]")
	}
	c, err := a.loadCluster(ctx, cf)
	if err != nil {
		return err
	}
	secrets, err := a.loadSecrets(ctx, sf, cf.flake)
	if err != nil {
		return err
	}
	k := secrets.Kubernetes
	admin, err := kpki.IssueAdmin(secrets.KubeconfigCA(), f.name, f.ttl, time.Now())
	if err != nil {
		return err
	}
	kc := kpki.Kubeconfig{Name: c.manifest.Cluster.Name, Server: c.manifest.Cluster.Endpoint, CA: []byte(k.CABundle()), Client: admin}
	if f.server != "" {
		endpoint, err := url.Parse(c.manifest.Cluster.Endpoint)
		if err != nil {
			return fmt.Errorf("the cluster endpoint: %w", err)
		}
		kc.Server, kc.ServerName = f.server, endpoint.Hostname()
	}
	data, err := kc.Encode()
	if err != nil {
		return err
	}
	if f.out == "-" {
		_, err := a.stdout.Write(data)
		return err
	}
	if f.force {
		if err := os.Remove(f.out); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	// The file holds the admin's private key.
	if err := writeNew(f.out, data, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "wrote %s; its certificate is valid for %s\n", f.out, f.ttl)
	return nil
}
