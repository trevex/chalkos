package main

import (
	"errors"
	"flag"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/storage/node"
)

// runGenerate is the systemd generator: it writes units into the generator's normal directory.
func runGenerate(args []string) error {
	flags := flag.NewFlagSet("generate", flag.ContinueOnError)
	stateDir := flags.String("state", "/state", "where STATE is mounted")
	cryptsetup := flags.String("cryptsetup", "systemd-cryptsetup", "absolute path of systemd-cryptsetup")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 && flags.NArg() != 3 {
		return errors.New("generate: want the generator directories NORMAL [EARLY LATE]")
	}
	units, err := node.Generate(filepath.Join(*stateDir, "storage"), *cryptsetup)
	if err != nil {
		return err
	}
	return units.Write(flags.Arg(0))
}
