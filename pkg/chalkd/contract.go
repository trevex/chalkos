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

// Contract returns the running image's schemas and kubernetes.json, as files, so a client checks
// config against exactly what chalkd checks it against.
func (s *Server) Contract(_ context.Context, _ *connect.Request[nodev1.ContractRequest]) (*connect.Response[nodev1.ContractResponse], error) {
	entries, err := os.ReadDir(s.Identity.Schemas)
	if err != nil {
		return nil, failed(connect.CodeFailedPrecondition, "read the image's schemas: %v", err)
	}
	resp := &nodev1.ContractResponse{Schemas: map[string][]byte{}}
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
