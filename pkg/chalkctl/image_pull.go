package chalkctl

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/imagepull"
	"github.com/trevex/chalkos/pkg/internal/cli"
)

func (a *app) imagePullCommand() *cobra.Command {
	var layout bool
	cmd := a.command(&cobra.Command{
		Use:         "pull <reference> <dir>",
		Short:       "Pull an image from a registry into an image directory or an OCI layout",
		Long:        imagePullLong,
		Example:     imagePullExample,
		Args:        cli.ExactArgs(2),
		Annotations: layer.Meta{Credentials: layer.NoCredentials}.Annotations(),
	}, func(a *app, ctx context.Context, pos []string) error { return a.imagePull(ctx, pos[0], pos[1], layout) })
	cmd.Flags().BoolVar(&layout, "layout", false, "write the OCI layout the registry serves, whose manifest keeps its digest, instead of an image directory")
	return cmd
}

// imagePull pulls the image ref names into dir and prints the reference by digest it resolved.
func (a *app) imagePull(ctx context.Context, ref, dir string, layout bool) error {
	pull := imagepull.Directory
	if layout {
		pull = imagepull.Layout
	}
	p, err := pull(ctx, ref, dir)
	if err != nil {
		return err
	}
	switch {
	case p.Present:
		fmt.Fprintf(a.stdout, "%s holds %s already; nothing pulled\n", dir, p.Reference)
	case p.Reference == ref:
		fmt.Fprintf(a.stdout, "pulled %s into %s\n", ref, dir)
	default:
		fmt.Fprintf(a.stdout, "pulled %s as %s into %s\n", ref, p.Reference, dir)
	}
	return nil
}
