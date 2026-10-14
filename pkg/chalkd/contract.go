package chalkd

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
)

// Contract returns what the running image was built for, and its schemas and kubernetes.json as
// files, so a client checks config against exactly what chalkd translates it for.
func (s *Server) Contract(_ context.Context, _ *connect.Request[nodev1.ContractRequest]) (*connect.Response[nodev1.ContractResponse], error) {
	// The installer runs an image of its own: config is for the image it installs, which comes
	// with its contract.
	if s.Installer {
		return nil, failed(connect.CodeFailedPrecondition, "an installer has no image to configure; take the schemas from the image you install")
	}
	entries, err := os.ReadDir(s.Identity.Schemas)
	if err != nil {
		return nil, failed(connect.CodeFailedPrecondition, "read the image's schemas: %v", err)
	}
	release := readOSRelease(s.Paths.OSRelease)
	resp := &nodev1.ContractResponse{
		Schemas:  map[string][]byte{},
		Cluster:  release["CHALKOS_CLUSTER"],
		Role:     release["CHALKOS_ROLE"],
		Platform: release["CHALKOS_PLATFORM"],
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Identity.Schemas, e.Name()))
		if err != nil {
			return nil, failed(connect.CodeInternal, "%v", err)
		}
		resp.Schemas[e.Name()] = data
	}
	data, err := os.ReadFile(s.Paths.KubernetesCluster)
	switch {
	case err == nil:
		resp.KubernetesCluster = data
	case !errors.Is(err, fs.ErrNotExist):
		return nil, failed(connect.CodeInternal, "%v", err)
	}
	return connect.NewResponse(resp), nil
}
