// Command docgen writes the pages of chalkos's documentation that are generated from its command
// line tools and config schemas, and checks the commands, config documents and Nix files its pages
// show.
//
//	docgen cli DIR ZENSICAL_TOML
//
// writes a page per command of chalkctl, chalklab, chalkbuild and chalkcfg to DIR and their
// navigation to the region of ZENSICAL_TOML between the lines "# BEGIN docgen cli" and
// "# END docgen cli".
//
//	docgen kinds PAGE SCHEMAS_DIR [SCHEMA_FILE...]
//
// writes the page of config kinds to PAGE from the JSON Schemas in SCHEMAS_DIR, an image's schemas
// directory, and the schema files given, those of chalkctl's and chalklab's own files.
//
//	docgen check DOCS_DIR
//
// fails when a shell code block of the Markdown files under DOCS_DIR, but not under
// DOCS_DIR/superpowers, runs a chalkctl, chalklab, chalkbuild or chalkcfg command or passes a flag
// they do not have.
//
//	docgen yaml DOCS_DIR SCHEMAS_DIR
//
// fails when a YAML block of those files holds a config document of a chalkos group, or of a group
// the schemas declare, that the schemas in SCHEMAS_DIR refuse.
//
//	docgen nix DOCS_DIR OUT_DIR
//
// writes the Nix blocks of those files that are whole files, titled with a .nix file name, to
// OUT_DIR, for a Nix parser to check.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/chalkbuild"
	"github.com/trevex/chalkos/pkg/chalkcfg"
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
	case len(args) >= 3 && args[0] == "kinds":
		return writeKinds(args[1], args[2], args[3:])
	case len(args) == 2 && args[0] == "check":
		return check(programs, args[1])
	case len(args) == 3 && args[0] == "yaml":
		return checkYAML(args[1], args[2])
	case len(args) == 3 && args[0] == "nix":
		return writeNix(args[1], args[2])
	}
	return errors.New("usage: docgen cli DIR ZENSICAL_TOML | docgen kinds PAGE SCHEMAS_DIR [SCHEMA_FILE...] | docgen check DOCS_DIR | docgen yaml DOCS_DIR SCHEMAS_DIR | docgen nix DOCS_DIR OUT_DIR")
}

// programs are the command trees of chalkctl, chalklab, chalkbuild and chalkcfg, with the help and
// completion commands cobra adds when a program runs. The completion command's page gets a
// description of docgen's: cobra creates the command as the program runs, bound to its output, so
// the programs cannot describe it themselves. chalkcfg disables its completion command, as nodes
// have no logins.
func programs() []*cobra.Command {
	roots := []*cobra.Command{chalkctl.NewCommand(), chalklab.NewCommand(), chalkbuild.NewCommand(), chalkcfg.NewCommand()}
	for _, root := range roots {
		root.InitDefaultHelpCmd()
		if root.CompletionOptions.DisableDefaultCmd {
			continue
		}
		root.InitDefaultCompletionCmd()
		completion, _, _ := root.Find([]string{"completion"})
		completion.Long = fmt.Sprintf(completionLong, root.Name())
		completion.Example = fmt.Sprintf(completionExample, root.Name())
	}
	return roots
}

// The description and example of a program's completion command, by the program's name.
const (
	completionLong = `Writes a script that completes %[1]s's commands and flags in a shell. Each shell's command
says how to load the script. The command needs no secrets file or client file.`
	completionExample = `  # Complete %[1]s's commands in every new bash session.
  %[1]s completion bash > ~/.local/share/bash-completion/completions/%[1]s`
)
