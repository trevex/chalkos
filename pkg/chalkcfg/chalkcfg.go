package chalkcfg

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/internal/cli"
)

// Where chalkd writes the node's documents, and where the image keeps its kinds' schemas.
const (
	configDir  = "/run/chalkos/config"
	schemasDir = "/etc/chalkos/schemas"
)

// NewCommand returns chalkcfg's root command, which reads the documents of the node it runs on.
func NewCommand() *cobra.Command {
	return newCommand(store{config: configDir, schemas: schemasDir})
}

func newCommand(s store) *cobra.Command {
	root := cli.Root(&cobra.Command{
		Use:     "chalkcfg",
		Short:   "Print the config documents of the node chalkcfg runs on",
		Long:    rootLong,
		Example: rootExample,
	})
	// Nodes have no logins to complete commands in.
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(getCommand(s))
	return root
}

// Execute runs chalkcfg's command tree with ctx, prints its error and returns the exit status:
// 2 and the command's usage after a usage error, 1 after another error.
func Execute(ctx context.Context, root *cobra.Command) int {
	return cli.Execute(ctx, root, cli.Options{})
}
