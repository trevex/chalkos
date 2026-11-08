package chalkd

import (
	"context"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
)

// Contract returns what the running image was built for, its layers, its schemas as files and its
// layers' facts, so a client checks config against exactly what chalkd translates it for.
func (s *Server) Contract(_ context.Context, _ *connect.Request[nodev1.ContractRequest]) (*connect.Response[nodev1.ContractResponse], error) {
	// The installer runs an image of its own: config is for the image it installs, which comes
	// with its contract.
	if s.Installer {
		return nil, layer.Errorf(connect.CodeFailedPrecondition, "an installer has no image to configure; take the schemas from the image you install")
	}
	release := readOSRelease(s.Paths.OSRelease)
	resp := &nodev1.ContractResponse{
		Schemas:  s.Loader.Contract.Schemas.Files(),
		Variant:  release["CHALKOS_VARIANT"],
		Platform: release["CHALKOS_PLATFORM"],
		Layers:   s.Plugins.Layers().Names(),
	}
	var err error
	if resp.LayerFacts, err = imageFacts(layer.Active[layer.ImageFacts](s.Plugins), layer.ImageFacts.RunningFacts); err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
