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
	// A section per API group, chalkos.dev's first and the others by name, each kind within its
	// group, then the files given in a section of their own.
	order := []string{
		"\n# Config kinds\n\nThis page is for operators who write",
		"\n## chalkos.dev { #chalkos-dev }\n\nThe kinds of `chalkos.dev` ",
		"\n### Node { #node }\n",
		"\n## networkd.chalkos.dev { #networkd-chalkos-dev }\n\n",
		"\n### Link { #link }\n",
		"\n## storage.chalkos.dev { #storage-chalkos-dev }\n\n",
		"\n### Volume { #volume }\n",
		"\n## Inventory and lab files { #inventory-and-lab-files }\n\n",
		"\n### Inventory { #inventory }\n",
		"\nSources: ",
	}
	last := -1
	for _, h := range order {
		i := strings.Index(got, h)
		if i <= last {
			t.Errorf("%q at %d, after %d", h, i, last)
		}
		last = i
	}
	for _, want := range []string{
		"### Node { #node }\n\nThe node's identity.\n\n`apiVersion: chalkos.dev/v1alpha1`\n\nA node's config holds at most one. It has 2 [change rules](builtins.md#rules-node).\n\n| Field | Type | Default | Description |\n| --- | --- | --- | --- |\n",
		"| `spec.hostname` | string | required | Host name set at boot. |\n",
		"| `spec.type` | one of `controlplane`, `worker` or null | `null` | What the node is in Kubernetes. |\n",
		"### Volume { #volume }\n\nA partition for the node's data.\n\n`apiVersion: storage.chalkos.dev/v1alpha1`\n\nA node's config may hold several, each with a name of its own.\n\n| Field |",
		// An object with a description keeps its row; one that only groups its fields has none.
		"| `spec.disk` | object | — | The disk the volume is on. |\n| `spec.disk.path` | string | — | Path below /dev, as &lt;path>. |\n| `spec.encryption.mode` | one of `tpm2`, `none` | `\"tpm2\"` | Encryption of the volume. |\n| `spec.extra` |",
		"| `spec.labels` | map of string | `{}` | Labels \\| with a pipe. |\n",
		"| `spec.password` | secret reference or null | `null` |  |\n",
		"| `spec.extra` | object, any fields | — | Any fields, as `a|b`. |\n| `spec.labels` |",
		"| `spec.mode` | `l2` | `\"l2\"` |  |\n| `spec.objects` | list of Kubernetes object | — |  |\n| `spec.password` |",
		"| `spec.size` | integer from 1 to 100 | — |  |\n",
		"| `spec.taints` | list of object | — |  |\n| `spec.taints[].effect` | one of `NoSchedule`, `NoExecute` | — |  |\n| `spec.taints[].key` | string | required |  |\n",
		"### Link { #link }\n\nA systemd .link file.\n\n`apiVersion: networkd.chalkos.dev/v1alpha1`\n\n",
		"| Field | networkd section | Keys |\n| --- | --- | --- |\n| `spec.match` | Match | `macAddress` (MACAddress), `name` (Name) |\n| `spec.sriov` | SR-IOV | `vlanId` (VLANId) |\n",
		"### Inventory { #inventory }\n\nThe nodes of a cluster.\n\n`apiVersion: chalkctl.chalkos.dev/v1alpha1`\n\n| Field |",
		"| `spec.files` | list of string | — | Config files every node gets. |\n| `spec.nodes` | map of object | — |  |\n| `spec.nodes.<name>.address` | string | — | Where chalkctl reaches the node. |\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks:\n%s", want)
		}
	}
	if strings.Contains(got, "| `spec.encryption` |") {
		t.Errorf("the page lists an object that only groups its fields:\n%s", got)
	}
	if strings.Contains(got, "see below") || strings.Contains(got, "| none |") {
		t.Errorf("the page writes a missing default other than as —:\n%s", got)
	}
	if inv := got[strings.Index(got, "### Inventory"):]; strings.Contains(inv, "A node's config") {
		t.Errorf("the inventory's section counts it as a node's config:\n%s", inv)
	}
	if volume := got[strings.Index(got, "### Volume"):strings.Index(got, "## Inventory and lab files")]; strings.Contains(volume, "change rule") {
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

func TestWriteKindsRefusesAKindWithoutADescription(t *testing.T) {
	dir := t.TempDir()
	schema := `{"x-chalkos-kind": {"group": "a.example.org", "version": "v1", "kind": "Rack"}}`
	if err := os.WriteFile(filepath.Join(dir, "a.example.org_v1_Rack.json"), []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}
	err := writeKinds(filepath.Join(t.TempDir(), "kinds.md"), dir, nil)
	if err == nil || !strings.Contains(err.Error(), "a.example.org_v1_Rack.json: the kind Rack has no description") {
		t.Errorf("error %v, want one naming the schema without a description", err)
	}
}

func TestWriteKindsRefusesASharedAnchor(t *testing.T) {
	dir := t.TempDir()
	for _, group := range []string{"a.example.org", "b.example.org"} {
		schema := `{"description": "A rack.", "x-chalkos-kind": {"group": "` + group + `", "version": "v1", "kind": "Rack"}}`
		if err := os.WriteFile(filepath.Join(dir, group+"_v1_Rack.json"), []byte(schema), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	err := writeKinds(filepath.Join(t.TempDir(), "kinds.md"), dir, nil)
	if err == nil || !strings.Contains(err.Error(), "#rack") {
		t.Errorf("error %v, want one naming the anchor #rack", err)
	}
}
