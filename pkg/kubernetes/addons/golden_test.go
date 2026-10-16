package addons

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/trevex/chalkos/pkg/kubernetes"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// TestRenderGolden renders the addons of clusters that differ in their families and their pod
// network, each from its testdata/<setup>/cluster.json: dual stack with flannel and an MTU,
// IPv4 alone, IPv6 alone, and IPv6 primary without flannel and with another cluster domain.
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
			var b bytes.Buffer
			enc := json.NewEncoder(&b)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			if err := enc.Encode(objects); err != nil {
				t.Fatal(err)
			}
			got := b.Bytes()
			golden := filepath.Join(dir, "addons.json")
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Errorf("the addons differ from %s; run go test ./pkg/kubernetes/addons -update and review the diff:\n%s", golden, got)
			}
		})
	}
}
