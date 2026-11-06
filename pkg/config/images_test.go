package config

import (
	"strings"
	"testing"
)

// Every image reference with a tag and no digest draws a line naming its document and field; a
// pinned one draws none.
func TestUnpinnedImages(t *testing.T) {
	const digest = "@sha256:e190f914a6cc21cab9268485d1ae7282fdeab1315ac64b668aa42d732fd1061d"
	pinned := map[string]string{}
	for field, ref := range testImages {
		if field != "proxy" {
			pinned[field] = ref + digest
		}
	}
	s := kubernetesSet(t, "{images: "+images(t, pinned)+"}", "")
	got := s.Documents().UnpinnedImages(allSchemas(t))
	want := `Cluster "default": spec.images.proxy: registry.k8s.io/kube-proxy:v1.37.1 names a tag but no digest, so nodes pull whatever the tag points to when they pull; pin it as registry.k8s.io/kube-proxy:v1.37.1@sha256:<digest>`
	if len(got) != 1 || got[0] != want {
		t.Errorf("UnpinnedImages() = %q, want [%q]", got, want)
	}
	all := kubernetesSet(t, "{}", "").Documents().UnpinnedImages(allSchemas(t))
	if len(all) != len(testImages) || !strings.Contains(strings.Join(all, "\n"), `Cluster "default": spec.images.flannel: `+testImages["flannel"]+" names a tag") {
		t.Errorf("UnpinnedImages() = %q, want a line for each of the Cluster's images", all)
	}
}

// The pod sandbox's image is the Cluster document's; a node without one has none.
func TestSandboxImage(t *testing.T) {
	if got, err := kubernetesSet(t, "{}", "").Documents().SandboxImage(); err != nil || got != testImages["pause"] {
		t.Errorf("SandboxImage() = %q, %v, want %q", got, err, testImages["pause"])
	}
	s, err := NewSet(mustParse(t, minimal), allSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Documents().SandboxImage(); err != nil || got != "" {
		t.Errorf("without a Cluster: SandboxImage() = %q, %v, want none", got, err)
	}
}
