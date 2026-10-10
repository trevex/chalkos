// Command chalkctl builds, signs, installs and operates chalkos clusters.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/trevex/chalkos/pkg/chalkctl"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	status := chalkctl.Execute(ctx, chalkctl.NewCommand())
	stop()
	os.Exit(status)
}
