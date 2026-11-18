package translate

import (
	"net/netip"
	"slices"
	"testing"
)

// registryDoc is a ClusterRegistry document called cluster with the spec given as YAML.
func registryDoc(spec string) string {
	return "---\napiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: ClusterRegistry\nmetadata: {name: cluster}\nspec: " + spec + "\n"
}

// TestClusterRegistry reads the ClusterRegistry with the schema's default port, none without one,
// and refuses one whose mirrors name a host twice under two spellings.
func TestClusterRegistry(t *testing.T) {
	r, ok, err := ClusterRegistry(parseDocs(t, plainHost+registryDoc(`{mirrors: [docker.io], endpoints: ["fd00::11"]}`)))
	if err != nil || !ok {
		t.Fatalf("ClusterRegistry = %v, %v", ok, err)
	}
	if r.Port != 5000 || !slices.Equal(r.Mirrors, []string{"docker.io"}) || !slices.Equal(r.Endpoints, []netip.Addr{netip.MustParseAddr("fd00::11")}) {
		t.Errorf("ClusterRegistry = %+v", r)
	}
	if _, ok, err := ClusterRegistry(parseDocs(t, plainHost)); ok || err != nil {
		t.Errorf("without a ClusterRegistry: %v, %v", ok, err)
	}
	_, _, err = ClusterRegistry(parseDocs(t, registryDoc(`{mirrors: [docker.io, index.docker.io], endpoints: ["fd00::11"]}`)))
	const want = `ClusterRegistry "cluster": mirrors: docker.io and index.docker.io name the same registry docker.io; name each host once`
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}
