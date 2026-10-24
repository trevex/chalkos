// Package chalkbuild is the chalkbuild command, which prepares the images Nix builds for a
// chalkos cluster's nodes: it writes them as OCI image layouts, signs copies of them for Secure
// Boot and pushes layouts to registries. NewCommand returns its command tree.
package chalkbuild

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/internal/cli"
)

// NewCommand returns chalkbuild's root command.
func NewCommand() *cobra.Command {
	root := cli.Root(&cobra.Command{
		Use:     "chalkbuild",
		Short:   "Prepare the images Nix builds for a chalkos cluster's nodes",
		Long:    rootLong,
		Example: rootExample,
		// A flag before a subcommand's name is parsed as the root's, which has none: a usage error.
		TraverseChildren: true,
	})
	root.AddCommand(ociCommand(), pushCommand(), signCommand())
	return root
}

// Execute runs chalkbuild's command tree with ctx, prints its error and returns the exit status:
// 2 and the command's usage after a usage error, 1 after another error.
func Execute(ctx context.Context, root *cobra.Command) int {
	return cli.Execute(ctx, root, cli.Options{})
}
