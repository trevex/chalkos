package translate

import (
	"testing"

	"github.com/trevex/chalkos/pkg/internal/golden"
	"github.com/trevex/chalkos/pkg/kubernetes"
)

// homelabImage is the Kubernetes facts of the homelab's images.
var homelabImage = kubernetes.Image{Version: "1.37.2", CNI: kubernetes.CNIFlannel}

// The homelab worker's documents on the homelab's image give the whole cluster in
// testdata/homelab-w1-cluster.json.
func TestClusterHomelabWorker(t *testing.T) {
	c, err := Cluster(homelabSet(t, "w1").Documents(), homelabImage)
	if err != nil {
		t.Fatal(err)
	}
	golden.Check(t, "testdata/homelab-w1-cluster.json", golden.JSON(t, c))
}
