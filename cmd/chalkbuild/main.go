// Command chalkbuild prepares the images Nix builds for a chalkos cluster's nodes.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/trevex/chalkos/pkg/chalkbuild"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	status := chalkbuild.Execute(ctx, chalkbuild.NewCommand())
	stop()
	os.Exit(status)
}
