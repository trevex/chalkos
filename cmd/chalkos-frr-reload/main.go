// Command chalkos-frr-reload applies a changed frr.conf to the running FRR without restarting it,
// so that the BGP sessions the change leaves alone stay up. frr.service runs it on a reload.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/frr"
	"github.com/trevex/chalkos/pkg/tool"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("chalkos-frr-reload: ")
	vtysh := flag.String("vtysh", "vtysh", "the vtysh to run")
	dryRun := flag.Bool("n", false, "print the commands that apply the file, and run none")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: chalkos-frr-reload [-n] [-vtysh PATH] FILE")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(context.Background(), *vtysh, flag.Arg(0), *dryRun); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, vtysh, file string, dryRun bool) error {
	text, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	target, err := frr.Load(string(text))
	if err != nil {
		return err
	}
	r := tool.ExecRunner{}
	if !dryRun {
		return frr.Reload(ctx, r, vtysh, filepath.Dir(file), target, log.Default())
	}
	running, err := frr.Running(ctx, r, vtysh)
	if err != nil {
		return err
	}
	fmt.Print(frr.Diff(running, target))
	return nil
}
