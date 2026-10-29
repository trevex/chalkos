// Package router is chalkctl's router layer: the command that shows a router's routes, and the
// router's section of a node's status.
package router

import (
	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/layers"
)

// Plugin is chalkctl's router layer.
type Plugin struct{}

var (
	_ layer.Plugin   = (*Plugin)(nil)
	_ layer.Commands = (*Plugin)(nil)
	_ layer.Status   = (*Plugin)(nil)
)

// New returns chalkctl's router layer.
func New() *Plugin { return &Plugin{} }

// Name is the router layer's.
func (*Plugin) Name() string { return layers.Router }

// Commands are node routes.
func (*Plugin) Commands(h layer.Host) map[string][]*cobra.Command {
	return map[string][]*cobra.Command{"node": {nodeRoutesCommand(h)}}
}
