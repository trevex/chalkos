package chalkd

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/internal/cli"
)

// NewCommand returns chalkd's command tree: its own commands cmds, and below a command named after
// each compiled plugin's layer, the plugin's commands, run as chalkd <layer> <name> whether or not
// the image carries the layer.
func NewCommand(reg *layer.Registry, cmds ...*cobra.Command) *cobra.Command {
	root := cli.Root(&cobra.Command{
		Use:   "chalkd",
		Short: "The chalkos node agent",
		Long: "chalkd is the chalkos node agent. It serves the node API on port 50000 and applies the node's\n" +
			"config at boot. The commands below a layer's name are those its units run.",
	})
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(cmds...)
	for _, p := range reg.Compiled() {
		c, ok := p.(layer.Commands)
		if !ok {
			continue
		}
		group := cli.Group(&cobra.Command{Use: p.Name(), Short: "Commands of the " + p.Name() + " layer"})
		group.AddCommand(c.Commands()...)
		root.AddCommand(group)
	}
	return root
}

// Execute runs chalkd's command tree root, prints its error and returns the exit status: 0 after
// success, 2 and the command's usage after a usage error, 1 after another error, whose joined
// errors are printed one per line.
func Execute(root *cobra.Command) int {
	return cli.Execute(context.Background(), root, cli.Options{SplitJoined: true})
}
