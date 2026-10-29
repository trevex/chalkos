package router

import (
	"path/filepath"
	"testing"

	routerv1 "github.com/trevex/chalkos/pkg/api/router/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer/layertest"
	"github.com/trevex/chalkos/pkg/layers"
)

// TestRouterAPIPermissions holds the router layer's API to the plugin's permissions as chalkd's
// TestNodeAPIPermissions holds the node API.
func TestRouterAPIPermissions(t *testing.T) {
	p := New(runnerNode{}, Options{})
	service := routerv1.File_chalkos_router_v1_router_proto.Services().ByName("RouterService")
	layertest.CheckPermissions(t, service, p.Permissions())
	layertest.CheckComments(t, filepath.Join("..", "..", "..", "..", "api", "chalkos", "router", "v1", "router.proto"), service, p.Permissions(), layers.Router)
}
