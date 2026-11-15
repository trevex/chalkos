package addons

import (
	"path/filepath"
	"testing"

	"github.com/trevex/chalkos/pkg/internal/golden"
	"github.com/trevex/chalkos/pkg/kubernetes"
)

// TestRenderGolden renders the addons of clusters that differ in their families and their pod
// network, each from its testdata/<setup>/cluster.json: dual stack with flannel, an MTU and ranges
// other than the defaults, IPv4 alone, IPv6 alone, and IPv6 primary without flannel and with
// another cluster domain. Review the order of the objects, flanneld's --public-ip arguments and
// the kube-dns family in a diff.
func TestRenderGolden(t *testing.T) {
	setups, err := filepath.Glob(filepath.Join("testdata", "*", "cluster.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(setups) != 4 {
		t.Fatalf("setups = %v", setups)
	}
	for _, path := range setups {
		dir := filepath.Dir(path)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			c, err := kubernetes.ReadCluster(path)
			if err != nil {
				t.Fatal(err)
			}
			objects, err := Render(c)
			if err != nil {
				t.Fatal(err)
			}
			golden.Check(t, filepath.Join(dir, "addons.json"), golden.JSON(t, objects))
		})
	}
}
