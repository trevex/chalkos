package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/internal/cli"
	sdkkubernetes "github.com/trevex/chalkos/pkg/sdk/layers/kubernetes"
)

type kubeconfigFlags struct {
	cluster                    layer.ClusterFlags
	secrets                    layer.SecretsFlags
	ttl                        time.Duration
	out, server, name, context string
	// contextSet is whether --context was given, so that an empty name is refused rather than
	// replaced by the default.
	contextSet bool
	overwrite  bool
}

func kubeconfigCommand(h layer.Host) *cobra.Command {
	var f kubeconfigFlags
	var cmd *cobra.Command
	cmd = h.Command(&cobra.Command{
		Use:         "kubeconfig",
		Short:       "Write an admin kubeconfig",
		Long:        kubeconfigLong,
		Example:     kubeconfigExample,
		Args:        cli.NoArgs,
		Annotations: layer.Meta{Credentials: layer.SecretsFile}.Annotations(),
	}, func(h layer.Host, ctx context.Context, pos []string) error {
		f.contextSet = cmd.Flags().Changed("context")
		return kubeconfig(h, ctx, f)
	})
	fs := cmd.Flags()
	f.cluster.Register(fs)
	f.secrets.Register(fs)
	fs.DurationVar(&f.ttl, "ttl", 24*time.Hour, "validity of the admin certificate")
	fs.StringVar(&f.out, "out", "kubeconfig", "file to write, - for standard output")
	fs.BoolVar(&f.overwrite, "overwrite", false, "replace an existing file")
	fs.StringVar(&f.server, "server", "", "URL clients reach the API server at, when it differs from the cluster endpoint; the certificate is still verified for the endpoint")
	fs.StringVar(&f.name, "name", "admin", "user name in the admin certificate")
	fs.StringVar(&f.context, "context", "", "name of the kubeconfig's cluster, user and context (default the inventory's metadata.name)")
	return cmd
}

func kubeconfig(h layer.Host, ctx context.Context, f kubeconfigFlags) error {
	c, err := h.Cluster(ctx, f.cluster)
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
	server, err := endpoint(c)
	if err != nil {
		return err
	}
	if server == "" {
		return fmt.Errorf("no Cluster document of the cluster %s names an endpoint, which a kubeconfig points clients at", c.Name())
	}
	secrets, err := h.Secrets(ctx, f.secrets)
	if err != nil {
		return err
	}
	data, err := sdkkubernetes.Kubeconfig(secrets, sdkkubernetes.KubeconfigRequest{
		Context: contextName, Server: server, Via: f.server, User: f.name, TTL: f.ttl,
	}, time.Now())
	if err != nil {
		return err
	}
	if f.out == "-" {
		_, err := h.Stdout().Write(data)
		return err
	}
	write := atomicfile.WriteNew
	if f.overwrite {
		write = atomicfile.WriteFile
	}
	// The file holds the admin's private key: a directory created for it is its owner's alone.
	if err := os.MkdirAll(filepath.Dir(f.out), 0o700); err != nil {
		return err
	}
	if err := write(f.out, data, 0o600); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s exists; pass --overwrite to replace it", f.out)
		}
		return err
	}
	fmt.Fprintf(h.Stdout(), "wrote %s; its certificate is valid for %s\n", f.out, f.ttl)
	return nil
}
