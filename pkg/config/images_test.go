package config

import (
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/kubernetes/kubetest"
)

// Every image reference with a tag and no digest draws a line naming its document and field; a
// pinned one draws none.
func TestUnpinnedImages(t *testing.T) {
	const digest = "@sha256:e190f914a6cc21cab9268485d1ae7282fdeab1315ac64b668aa42d732fd1061d"
	pinned := map[string]string{}
	for field, ref := range kubetest.DocumentImages {
		if field != "proxy" {
			pinned[field] = ref + digest
		}
	}
	s := kubernetesSet(t, "{images: "+kubetest.DocumentImagesWith(t, pinned)+"}")
	got := s.Documents().UnpinnedImages(allSchemas(t))
	want := `Cluster "default": images.proxy: registry.k8s.io/kube-proxy:v1.37.1 names a tag but no digest, so nodes pull whatever the tag points to when they pull; pin it as registry.k8s.io/kube-proxy:v1.37.1@sha256:<digest>`
	if len(got) != 1 || got[0] != want {
		t.Errorf("UnpinnedImages() = %q, want [%q]", got, want)
	}
	all := kubernetesSet(t, "{}").Documents().UnpinnedImages(allSchemas(t))
	if len(all) != len(kubetest.DocumentImages) || !strings.Contains(strings.Join(all, "\n"), `Cluster "default": images.flannel: `+kubetest.DocumentImages["flannel"]+" names a tag") {
		t.Errorf("UnpinnedImages() = %q, want a line for each of the Cluster's images", all)
	}
}

// kubernetesSet is minimal for a Kubernetes worker with the Cluster spec given.
func kubernetesSet(t *testing.T, spec string) *Set {
	t.Helper()
	s, err := NewSet(mustParse(t, minimal+kubetest.ClusterDoc("default", spec)+kubetest.NodeDoc("default", "{type: worker}")), allSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
