package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/internal/cli"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

type kubeconfigFlags struct {
	secrets                    layer.SecretsFlags
	ttl                        time.Duration
	out, server, name, context string
	// contextSet is whether --context was given, so that an empty name is refused rather than
	// replaced by the default.
	contextSet bool
	force      bool
}

func kubeconfigCommand(h layer.Host) *cobra.Command {
	var f kubeconfigFlags
	var cmd *cobra.Command
	cmd = h.Command(&cobra.Command{
		Use:     "kubeconfig",
		Short:   "Write an admin kubeconfig",
		Long:    kubeconfigLong,
		Example: kubeconfigExample,
		Args:    cli.NoArgs,
	}, func(h layer.Host, ctx context.Context, pos []string) error {
		f.contextSet = cmd.Flags().Changed("context")
		return kubeconfig(h, ctx, f)
	})
	fs := cmd.Flags()
	f.secrets = h.SecretsFlags(fs)
	fs.DurationVar(&f.ttl, "ttl", 8760*time.Hour, "validity of the admin certificate")
	fs.StringVar(&f.out, "out", "kubeconfig", "file to write, - for standard output")
	fs.BoolVar(&f.force, "force", false, "replace an existing file")
	fs.StringVar(&f.server, "server", "", "URL clients reach the API server at, when it differs from the cluster endpoint; the certificate is still verified for the endpoint")
	fs.StringVar(&f.name, "name", "admin", "user name in the admin certificate")
	fs.StringVar(&f.context, "context", "", "name of the kubeconfig's cluster, user and context (default the inventory's metadata.name)")
	return cmd
}

func kubeconfig(h layer.Host, ctx context.Context, f kubeconfigFlags) error {
	c, err := h.Cluster(ctx, f.secrets)
	if err != nil {
		return err
	}
	if err := c.RequireNodes("kubeconfig"); err != nil {
		return err
	}
	// A kubeconfig's names may hold almost any character; only an empty one names nothing.
	contextName := f.context
	switch {
	case f.contextSet && strings.TrimSpace(contextName) == "":
		return errors.New("--context needs a name")
	case !f.contextSet:
		contextName = c.InventoryName()
	}
	if c.Endpoint() == "" {
		return fmt.Errorf("no Cluster document of the cluster %s names an endpoint, which a kubeconfig points clients at", c.Name())
	}
	secrets, err := h.Secrets(ctx, f.secrets)
	if err != nil {
		return err
	}
	k := secrets.Kubernetes
	admin, err := kpki.IssueAdmin(secrets.KubeconfigCA(), f.name, f.ttl, time.Now())
	if err != nil {
		return err
	}
	kc := kpki.Kubeconfig{Name: contextName, Server: c.Endpoint(), CA: []byte(k.CABundle()), Client: admin}
	if f.server != "" {
		endpoint, err := url.Parse(c.Endpoint())
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
		_, err := h.Stdout().Write(data)
		return err
	}
	if f.force {
		if err := os.Remove(f.out); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	// The file holds the admin's private key.
	if err := atomicfile.WriteNew(f.out, data, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(h.Stdout(), "wrote %s; its certificate is valid for %s\n", f.out, f.ttl)
	return nil
}
