package router

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/trevex/chalkos/pkg/internal/cli"
	"github.com/trevex/chalkos/pkg/tool"
)

// loadFirewall loads the rendered ruleset with nft, or the ruleset fallback, which forwards
// nothing, when the node has none rendered, as in maintenance mode or when its config did not
// render. A rendered ruleset that nft refuses leaves the fallback in place and fails.
func (p *Plugin) loadFirewall(ctx context.Context, ruleset, fallback string, stderr io.Writer) error {
	loadFallback := func() error {
		if _, err := tool.Run(ctx, p.run, "nft", "-f", fallback); err != nil {
			return fmt.Errorf("load the fallback ruleset %s: %w", fallback, err)
		}
		return nil
	}
	if _, err := os.Stat(ruleset); err != nil {
		fmt.Fprintln(stderr, "the node has no rendered ruleset; loading the fallback ruleset, which forwards nothing")
		return loadFallback()
	}
	if _, err := tool.Run(ctx, p.run, "nft", "-f", ruleset); err == nil {
		return nil
	}
	fmt.Fprintf(stderr, "nft refused %s; loading the fallback ruleset, which forwards nothing\n", ruleset)
	if err := loadFallback(); err != nil {
		return err
	}
	return cli.Status(1)
}
