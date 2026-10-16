package inventory_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/inventory"
	"github.com/trevex/chalkos/pkg/manifest"
)

// fixtureBridges are the inventories of the e2e clusters' fixtures, by the set of
// CHALKOS_TEST_BRIDGE whose manifest defines the same nodes. nodes are the nodes the inventory
// names, nil for every node of the manifest: the inventories that change node chalklab's config
// name it alone.
var fixtureBridges = []struct {
	inventory, bridge string
	nodes             []string
}{
	{"../../test/fixtures/clusters/chalklab/chalkos.yaml", "base", nil},
	{"../../test/fixtures/clusters/chalklab/added.yaml", "added", []string{"chalklab"}},
	{"../../test/fixtures/clusters/chalklab/destructive.yaml", "destructive", []string{"chalklab"}},
	{"../../test/fixtures/clusters/chalklab-ha/chalkos.yaml", "ha", nil},
}

// TestFixturesGiveTheBridgesDocuments checks that the fixtures name the nodes they should, and
// that every node gets the documents the bridge generates for it from the cluster definitions the
// images are built from, so the e2e tests deliver the config they did. The Images' sources
// differ, a directory against a flake attribute, and are left out.
func TestFixturesGiveTheBridgesDocuments(t *testing.T) {
	root := os.Getenv("CHALKOS_TEST_BRIDGE")
	if root == "" {
		t.Skip("CHALKOS_TEST_BRIDGE is not set; the go-unit check sets it")
	}
	for _, fb := range fixtureBridges {
		f, err := os.Open(filepath.Join(root, fb.bridge, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		m, err := manifest.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		inv, err := inventory.Load(fb.inventory)
		if err != nil {
			t.Fatal(err)
		}
		want := fb.nodes
		if want == nil {
			want = slices.Sorted(maps.Keys(m.Nodes))
		}
		if got := inv.NodeNames(); !slices.Equal(got, want) {
			t.Errorf("%s names the nodes %q, want %q of the bridge's %s", fb.inventory, got, want, fb.bridge)
		}
		for _, name := range inv.NodeNames() {
			t.Run(fb.bridge+"/"+name, func(t *testing.T) {
				nc, err := inv.Node(name)
				if err != nil {
					t.Fatal(err)
				}
				b, err := config.FromManifest(m, name)
				if err != nil {
					t.Fatal(err)
				}
				if got, want := withoutSources(t, nc.Documents), withoutSources(t, b.Documents); got != want {
					t.Errorf("documents of %s =\n%s\nthe bridge's =\n%s", name, got, want)
				}
			})
		}
	}
}

// withoutSources is the documents as sorted JSON lines, each Image without its source.
func withoutSources(t *testing.T, docs []config.Document) string {
	t.Helper()
	var lines []string
	for _, d := range docs {
		obj := d.Object
		if d.APIVersion == config.CoreAPI && d.Kind == "Image" {
			spec := maps.Clone(obj["spec"].(map[string]any))
			delete(spec, "source")
			obj = maps.Clone(obj)
			obj["spec"] = spec
		}
		b, err := json.Marshal(obj)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}
