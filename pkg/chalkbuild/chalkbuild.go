// Package chalkbuild is the chalkbuild command, which prepares the images Nix builds for a
// chalkos cluster's nodes: it signs copies of them for Secure Boot. NewCommand returns its command
// tree.
package chalkbuild

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

// NewCommand returns chalkbuild's root command.
func NewCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "chalkbuild",
		Short:         "Prepare the images Nix builds for a chalkos cluster's nodes",
		Long:          rootLong,
		Example:       rootExample,
		SilenceErrors: true,
		SilenceUsage:  true,
		// A flag before a subcommand's name is parsed as the root's, which has none: a usage error.
		TraverseChildren: true,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("%w: unknown command %q for %q", errUsage, args[0], cmd.CommandPath())
			}
			return nil
		},
		RunE: func(*cobra.Command, []string) error { return errUsage },
	}
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		// On the root, the flag may as well be a misplaced subcommand's: a usage error.
		if cmd.HasSubCommands() {
			return fmt.Errorf("%w: %w", errUsage, err)
		}
		return fmt.Errorf("%w; %s --help lists its flags", err, cmd.CommandPath())
	})
	root.AddCommand(signCommand())
	return root
}

// Execute runs chalkbuild's command tree with ctx, prints its error and returns the exit status:
// 2 and the command's usage after a usage error, 1 after another error.
func Execute(ctx context.Context, root *cobra.Command) int {
	cmd, err := root.ExecuteContextC(ctx)
	// The root fails only with usage errors; another error of its own is a flag before a
	// subcommand's name, which cobra returns without the flag error function.
	if err != nil && !errors.Is(err, errUsage) && cmd.HasSubCommands() {
		err = fmt.Errorf("%w: %w", errUsage, err)
	}
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
