package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteKinds(t *testing.T) {
	page := filepath.Join(t.TempDir(), "kinds.md")
	if err := writeKinds(page, "testdata/kinds/schemas", []string{"testdata/kinds/inventory.json"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.HasPrefix(got, "---\ntitle: \"Config kinds\"\n") {
		t.Errorf("front matter: %q", got[:min(len(got), 80)])
	}
	// chalkos.dev's kinds first, the others by group, then the files given.
	order := []string{"\n## Node { #node }\n", "\n## Link { #link }\n", "\n## Volume { #volume }\n", "\n## Inventory { #inventory }\n"}
	last := -1
	for _, h := range order {
		i := strings.Index(got, h)
		if i <= last {
			t.Errorf("heading %q at %d, after %d", h, i, last)
		}
		last = i
	}
	for _, want := range []string{
		"## Node { #node }\n\n`apiVersion: chalkos.dev/v1alpha1`\n\nA node's config holds at most one.\n\nIt has 2 [change rules](../how-it-works/node/rules.md).\n\n| Field | Type | Default | Description |\n| --- | --- | --- | --- |\n",
		"| `spec.hostname` | string | required | Host name set at boot. |\n",
		"| `spec.type` | one of `controlplane`, `worker` or null | `null` | What the node is in Kubernetes. |\n",
		"`apiVersion: storage.chalkos.dev/v1alpha1`\n\nA node's config may hold several, each with a name of its own.\n\n",
		"| `spec.disk` | object | see below | The disk the volume is on. |\n| `spec.disk.path` | string | none | Path below /dev. |\n",
		"| `spec.labels` | map of string | `{}` | Labels \\| with a pipe. |\n",
		"| `spec.password` | secret reference or null | `null` |  |\n",
		"| `spec.extra` | object, any fields | none | Any fields. |\n| `spec.labels` |",
		"| `spec.mode` | `l2` | `\"l2\"` |  |\n| `spec.objects` | list of Kubernetes object | none |  |\n| `spec.password` |",
		"| `spec.size` | integer from 1 to 100 | none |  |\n",
		"| `spec.taints` | list of object | none |  |\n| `spec.taints[].effect` | one of `NoSchedule`, `NoExecute` | none |  |\n| `spec.taints[].key` | string | required |  |\n",
		"| Field | networkd section | Keys |\n| --- | --- | --- |\n| `spec.match` | Match | `macAddress` (MACAddress), `name` (Name) |\n| `spec.sriov` | SR-IOV | `vlanId` (VLANId) |\n",
		"## Inventory { #inventory }\n\n`apiVersion: chalkctl.chalkos.dev/v1alpha1`\n\nThe nodes of a cluster.\n\n| Field |",
		"| `spec.files` | list of string | none | Config files every node gets. |\n| `spec.nodes` | map of object | none |  |\n| `spec.nodes.<name>.address` | string | none | Where chalkctl reaches the node. |\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks:\n%s", want)
		}
	}
	if inv := got[strings.Index(got, "## Inventory"):]; strings.Contains(inv, "A node's config") {
		t.Errorf("the inventory's section counts it as a node's config:\n%s", inv)
	}
	if volume := got[strings.Index(got, "## Volume"):]; strings.Contains(volume, "change rule") {
		t.Errorf("the Volume's section names rules it has none of:\n%s", volume)
	}
}

func TestWriteKindsRefusesASchemaWithoutItsKind(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.json"), []byte(`{"type": "object"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := writeKinds(filepath.Join(t.TempDir(), "kinds.md"), dir, nil)
	if err == nil || !strings.Contains(err.Error(), "x.json: has no x-chalkos-kind") {
		t.Errorf("error %v, want one naming x.json", err)
	}
}

func TestWriteKindsRefusesASharedAnchor(t *testing.T) {
	dir := t.TempDir()
	for _, group := range []string{"a.example.org", "b.example.org"} {
		schema := `{"x-chalkos-kind": {"group": "` + group + `", "version": "v1", "kind": "Rack"}}`
		if err := os.WriteFile(filepath.Join(dir, group+"_v1_Rack.json"), []byte(schema), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	err := writeKinds(filepath.Join(t.TempDir(), "kinds.md"), dir, nil)
	if err == nil || !strings.Contains(err.Error(), "#rack") {
		t.Errorf("error %v, want one naming the anchor #rack", err)
	}
}
