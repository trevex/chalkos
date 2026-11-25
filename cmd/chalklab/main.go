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
	// The first signal ends the context; the next one kills the process.
	context.AfterFunc(ctx, stop)
	status := chalklab.Execute(ctx, chalklab.NewCommand())
	stop()
	os.Exit(status)
}
