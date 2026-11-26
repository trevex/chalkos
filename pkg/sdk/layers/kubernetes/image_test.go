package kubernetes

import (
	"context"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"

	"github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes/kubernetestest"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/ociimage/ocitest"
)

// imageRef pushes a chalkos image of the header and the contract to a fresh registry and returns
// its reference by digest.
func imageRef(t *testing.T, c ociimage.Contract) string {
	t.Helper()
	host := ocitest.Registry(t)
	h := ocitest.Header()
	c.Metadata = []byte(`{"rootHash": "` + h.RootHash + `", "platform": "` + h.Platform + `"}`)
	return ocitest.Push(t, ocitest.Image(t, h, c), host+"/chalkos/test-metal:v1")
}

// TestImageOfReadsTheKubeletRelease reads the kubelet release and the CNI an image's contract
// carries, from an image read back from a registry.
func TestImageOfReadsTheKubeletRelease(t *testing.T) {
	c := ocitest.Contract()
	c.Facts = map[string][]byte{"kubernetes": []byte(kubernetestest.Image)}
	ref := imageRef(t, c)
	art, err := ociimage.Read(context.Background(), ref, authn.DefaultKeychain)
	if err != nil {
		t.Fatal(err)
	}
	img, ok, err := ImageOf(art.Contract)
	if err != nil || !ok {
		t.Fatalf("ImageOf: %v, %v", ok, err)
	}
	if img.Version != "1.37.1" || img.CNI != k8s.CNIFlannel {
		t.Errorf("ImageOf = %+v", img)
	}
}

// TestImageOfRefusesAnImageWithoutKubernetes reports no Kubernetes layer for an image whose
// contract carries no facts/kubernetes.json, or an empty one.
func TestImageOfRefusesAnImageWithoutKubernetes(t *testing.T) {
	empty := ocitest.Contract()
	empty.Facts = map[string][]byte{"kubernetes": {}}
	for name, c := range map[string]ociimage.Contract{"no facts": ocitest.Contract(), "empty facts": empty} {
		art, err := ociimage.Read(context.Background(), imageRef(t, c), authn.DefaultKeychain)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok, err := ImageOf(art.Contract); err != nil || ok {
			t.Errorf("%s: ImageOf = ok %v, err %v; want ok false, err nil", name, ok, err)
		}
	}
}
