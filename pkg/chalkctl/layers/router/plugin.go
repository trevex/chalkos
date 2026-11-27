// Package router is chalkctl's router layer: the command that shows a router's routes, the
// router's section of a node's status, and the router's part in the SDK, which links the validator
// router.
package router

import (
	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/sdk"
	sdkrouter "github.com/trevex/chalkos/pkg/sdk/layers/router"
)

// Plugin is chalkctl's router layer.
type Plugin struct{ sdkrouter.Layer }

var (
	_ layer.Plugin   = (*Plugin)(nil)
	_ layer.Commands = (*Plugin)(nil)
	_ layer.Status   = (*Plugin)(nil)
	_ sdk.Validators = (*Plugin)(nil)
)

// Title is the router layer's, which titles its commands in chalkctl's help.
func (*Plugin) Title() string { return "Router" }

// Commands are node routes.
func (*Plugin) Commands(h layer.Host) map[string][]*cobra.Command {
	return map[string][]*cobra.Command{"node": {nodeRoutesCommand(h)}}
}
