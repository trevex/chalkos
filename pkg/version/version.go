// Package version holds the version of chalkos and the git revision a program was built from,
// which the build sets with the linker's -X flag, and the version command that prints them.
package version

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/internal/cli"
)

// Version is chalkos's version, as version.nix names it; Revision is the git revision the program
// was built from. Each is unknown in a build that did not set it, such as go build's.
var (
	Version  = "unknown"
	Revision = "unknown"
)

const long = `Prints the version of chalkos the program belongs to and the git revision it was built from.
A build from a working tree with uncommitted changes names the revision with -dirty after it, and
one that does not know its revision, such as a build outside a git checkout, names it unknown.
The command needs no secrets file or client file.`

// Command is the version command of the program's command tree.
func Command(program string) *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		Short:   "Print the version and the git revision of the build",
		Long:    long,
		Example: "  " + program + " version",
		Args:    cli.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "version %s\nrevision %s\n", Version, Revision)
			return err
		},
	}
}
