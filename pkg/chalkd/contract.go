package chalkd

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
)

// Contract returns what the running image was built for, its layers, its schemas as files and its
// layers' facts, so a client checks config against exactly what chalkd translates it for.
func (s *Server) Contract(_ context.Context, _ *connect.Request[nodev1.ContractRequest]) (*connect.Response[nodev1.ContractResponse], error) {
	// The installer runs an image of its own: config is for the image it installs, which comes
	// with its contract.
	if s.Installer {
		return nil, failed(connect.CodeFailedPrecondition, "an installer has no image to configure; take the schemas from the image you install")
	}
	entries, err := os.ReadDir(s.Loader.Schemas)
	if err != nil {
		return nil, failed(connect.CodeFailedPrecondition, "read the image's schemas: %v", err)
	}
	release := readOSRelease(s.Paths.OSRelease)
	resp := &nodev1.ContractResponse{
		Schemas:  map[string][]byte{},
		Variant:  release["CHALKOS_VARIANT"],
		Platform: release["CHALKOS_PLATFORM"],
		Layers:   s.Plugins.Layers().Names(),
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Loader.Schemas, e.Name()))
		if err != nil {
			return nil, failed(connect.CodeInternal, "%v", err)
		}
		resp.Schemas[e.Name()] = data
	}
	if facts, err := k8s.FactsPayloadOf(s.Loader.KubernetesImage); err != nil {
		return nil, failed(connect.CodeInternal, "%v", err)
	} else if facts != nil {
		resp.LayerFacts = append(resp.LayerFacts, facts)
	}
	return connect.NewResponse(resp), nil
}
