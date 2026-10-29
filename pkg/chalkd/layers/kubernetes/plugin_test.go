package kubernetes

import (
	"path/filepath"
	"testing"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer/layertest"
	"github.com/trevex/chalkos/pkg/layers"
)

// TestKubernetesAPIPermissions holds the Kubernetes layer's API to the plugin's permissions as
// chalkd's TestNodeAPIPermissions holds the node API.
func TestKubernetesAPIPermissions(t *testing.T) {
	p := New(nil, Options{})
	service := kubernetesv1.File_chalkos_kubernetes_v1_kubernetes_proto.Services().ByName("KubernetesService")
	layertest.CheckPermissions(t, service, p.Permissions())
	layertest.CheckComments(t, filepath.Join("..", "..", "..", "..", "api", "chalkos", "kubernetes", "v1", "kubernetes.proto"), service, p.Permissions(), layers.Kubernetes)
}
