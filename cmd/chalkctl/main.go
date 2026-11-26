// Command chalkctl installs and operates chalkos clusters.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/trevex/chalkos/pkg/chalkctl"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// The first signal ends the context; the next one kills the process.
	context.AfterFunc(ctx, stop)
	status := chalkctl.Execute(ctx, chalkctl.NewCommand())
	stop()
	os.Exit(status)
}
