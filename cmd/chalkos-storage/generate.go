package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/trevex/chalkos/pkg/storage"
	"github.com/trevex/chalkos/pkg/storage/node"
)

// runGenerate is the systemd generator: it writes units into the generator's normal directory.
func runGenerate(args []string) error {
	flags := flag.NewFlagSet("generate", flag.ContinueOnError)
	stateDir := flags.String("state", storage.StateDir, "where STATE is mounted")
	cryptsetup := flags.String("cryptsetup", "systemd-cryptsetup", "absolute path of systemd-cryptsetup")
	execPaths := map[string]string{}
	flags.Func("exec-path", "PATH=OPTIONS: a directory programs run from, mounted with the options; repeatable", func(s string) error {
		path, options, ok := strings.Cut(s, "=")
		if !ok || !strings.HasPrefix(path, "/") || options == "" {
			return fmt.Errorf("want PATH=OPTIONS with an absolute path, got %q", s)
		}
		execPaths[path] = options
		return nil
	})
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 && flags.NArg() != 3 {
		return errors.New("generate: want the generator directories NORMAL [EARLY LATE]")
	}
	st := &node.Storage{StateDir: *stateDir, BootPartitions: node.DefaultBootPartitions, ExecPaths: execPaths}
	units, err := st.Generate(*cryptsetup)
	if err != nil {
		return err
	}
	return units.Write(flags.Arg(0))
}
