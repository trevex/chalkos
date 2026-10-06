// Command chalkos-storage creates, unlocks and mounts the volumes of a chalkos node. In the
// initrd it opens STATE, then creates the volumes the node's storage section defines and opens
// VAR.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
)

const usage = `usage: chalkos-storage <mode>

modes:
  state   unlock STATE and mount it at /sysroot/state (initrd)
  initrd  create volumes, pin disks, and mount VAR at /sysroot/var (initrd)`

func main() {
	log.SetFlags(0)
	log.SetPrefix("chalkos-storage: ")
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "state":
		err = newBoot().openState(ctx)
	case "initrd":
		err = newBoot().setUp(ctx)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
