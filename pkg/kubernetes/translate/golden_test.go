package translate

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/trevex/chalkos/pkg/kubernetes"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// homelabImage is the Kubernetes facts of the homelab's images.
var homelabImage = kubernetes.Image{Version: "1.37.1", CNI: kubernetes.CNIFlannel}

// The homelab worker's documents on the homelab's image give the whole cluster in
// testdata/homelab-w1-cluster.json.
func TestClusterHomelabWorker(t *testing.T) {
	c, err := Cluster(homelabSet(t, "w1").Documents(), homelabImage)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(c); err != nil {
		t.Fatal(err)
	}
	golden := "testdata/homelab-w1-cluster.json"
	if *update {
		if err := os.WriteFile(golden, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if b.String() != string(want) {
		t.Errorf("the cluster differs from %s; run go test ./pkg/kubernetes/translate -update and review the diff:\n%s", golden, b.String())
	}
}
