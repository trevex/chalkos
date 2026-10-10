// Command chalklab runs the nodes of a chalkos cluster as QEMU virtual machines on this machine.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/trevex/chalkos/pkg/chalklab"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	status := chalklab.Execute(ctx, chalklab.NewCommand())
	stop()
	os.Exit(status)
}
