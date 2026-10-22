package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/identity"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

// kubernetesShare issues the share of a node with Kubernetes; nil for one without.
func kubernetesShare(t *target, now time.Time) ([]byte, error) {
	kind, err := t.cluster.kind(t.name)
	if err != nil || kind == "" {
		return nil, err
	}
	share, err := kpki.ShareFor(&t.secrets, kind, t.name, now)
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
	cmd.Flags().DurationVar(&timeout, "timeout", 20*time.Minute, "how long to wait for the control plane to apply its add-ons and Addon objects")
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
	// Without an inventory the node itself refuses when it is a worker.
	if !t.cluster.partial {
		kind, err := t.cluster.kind(t.name)
		if err != nil {
			return err
		}
		if kind != identity.KindControlPlane {
			return fmt.Errorf("%s is not a control-plane node; bootstrap one of the cluster's control-plane nodes", t.name)
		}
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
	cluster                    clusterFlags
	secrets                    secretFlags
	ttl                        time.Duration
	out, server, name, context string
	// contextSet is whether --context was given, so that an empty name is refused rather than
	// replaced by the default.
	contextSet bool
	force      bool
}

func (a *app) kubeconfigCommand() *cobra.Command {
	var f kubeconfigFlags
	var cmd *cobra.Command
	cmd = a.command(&cobra.Command{
		Use:     "kubeconfig",
		Short:   "Write an admin kubeconfig",
		Long:    kubeconfigLong,
		Example: kubeconfigExample,
	}, func(a *app, ctx context.Context, pos []string) error {
		f.contextSet = cmd.Flags().Changed("context")
		return a.kubeconfig(ctx, f, pos)
	})
	fs := cmd.Flags()
	f.cluster.register(fs)
	f.secrets.register(fs)
	fs.DurationVar(&f.ttl, "ttl", 8760*time.Hour, "validity of the admin certificate")
	fs.StringVar(&f.out, "out", "kubeconfig", "file to write, - for standard output")
	fs.BoolVar(&f.force, "force", false, "replace an existing file")
	fs.StringVar(&f.server, "server", "", "URL clients reach the API server at, when it differs from the cluster endpoint; the certificate is still verified for the endpoint")
	fs.StringVar(&f.name, "name", "admin", "user name in the admin certificate")
	fs.StringVar(&f.context, "context", "", "name of the kubeconfig's cluster, user and context (default the inventory's metadata.name)")
	return cmd
}

func (a *app) kubeconfig(ctx context.Context, f kubeconfigFlags, pos []string) error {
	cf, sf := f.cluster, f.secrets
	if len(pos) != 0 {
		return errors.New("usage: chalkctl kubeconfig [--ttl 8760h] [--out FILE] [--server URL] [--context NAME]")
	}
	c, err := loadCluster(ctx, cf)
	if err != nil {
		return err
	}
	if err := c.requireNodes("kubeconfig"); err != nil {
		return err
	}
	// A kubeconfig's names may hold almost any character; only an empty one names nothing.
	contextName := f.context
	switch {
	case f.contextSet && strings.TrimSpace(contextName) == "":
		return errors.New("--context needs a name")
	case !f.contextSet:
		contextName = c.inv.Name
	}
	if c.endpoint == "" {
		return fmt.Errorf("no Cluster document of the cluster %s names an endpoint, which a kubeconfig points clients at", c.name)
	}
	secrets, err := a.loadSecrets(ctx, sf)
	if err != nil {
		return err
	}
	k := secrets.Kubernetes
	admin, err := kpki.IssueAdmin(secrets.KubeconfigCA(), f.name, f.ttl, time.Now())
	if err != nil {
		return err
	}
	kc := kpki.Kubeconfig{Name: contextName, Server: c.endpoint, CA: []byte(k.CABundle()), Client: admin}
	if f.server != "" {
		endpoint, err := url.Parse(c.endpoint)
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
