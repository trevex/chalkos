package chalkcfg

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
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
	root := &cobra.Command{
		Use:           "chalkcfg",
		Short:         "Print the config documents of the node chalkcfg runs on",
		Long:          rootLong,
		Example:       rootExample,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("%w: unknown command %q for %q", errUsage, args[0], cmd.CommandPath())
			}
			return nil
		},
		RunE: func(*cobra.Command, []string) error { return errUsage },
	}
	// Nodes have no logins to complete commands in.
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fmt.Errorf("%w: %w", errUsage, err)
	})
	root.AddCommand(getCommand(s))
	return root
}

// Execute runs chalkcfg's command tree with ctx, prints its error and returns the exit status:
// 2 and the command's usage after a usage error, 1 after another error.
func Execute(ctx context.Context, root *cobra.Command) int {
	cmd, err := root.ExecuteContextC(ctx)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage):
		if err != errUsage {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", root.Name(), err)
		}
		fmt.Fprint(cmd.ErrOrStderr(), cmd.UsageString())
		return 2
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", root.Name(), err)
	return 1
}

var errUsage = errors.New("usage")
