// Command chalkcfg prints the config documents of the node it runs on.
package main

import (
	"context"
	"os"

	"github.com/trevex/chalkos/pkg/chalkcfg"
)

func main() {
	os.Exit(chalkcfg.Execute(context.Background(), chalkcfg.NewCommand()))
}
