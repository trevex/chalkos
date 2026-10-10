// Command docgen writes the pages of chalkos's documentation that are generated from its command
// line tools, and checks the commands its pages show.
//
//	docgen cli DIR ZENSICAL_TOML
//
// writes a page per command of chalkctl and chalklab to DIR and their navigation to the region
// of ZENSICAL_TOML between the lines "# BEGIN docgen cli" and "# END docgen cli".
//
//	docgen check DOCS_DIR
//
// fails when a shell code block of the Markdown files under DOCS_DIR, but not under
// DOCS_DIR/superpowers, runs a chalkctl or chalklab command or passes a flag they do not have.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/chalkctl"
	"github.com/trevex/chalkos/pkg/chalklab"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "docgen:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	switch {
	case len(args) == 3 && args[0] == "cli":
		return writeCLI(programs(), args[1], args[2])
	case len(args) == 2 && args[0] == "check":
		return check(programs, args[1])
	}
	return errors.New("usage: docgen cli DIR ZENSICAL_TOML | docgen check DOCS_DIR")
}

// programs are the command trees of chalkctl and chalklab, with the help and completion commands
// cobra adds when a program runs.
func programs() []*cobra.Command {
	roots := []*cobra.Command{chalkctl.NewCommand(), chalklab.NewCommand()}
	for _, root := range roots {
		root.InitDefaultHelpCmd()
		root.InitDefaultCompletionCmd()
	}
	return roots
}
