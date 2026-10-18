package chalkctl

import (
	"bytes"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
)

func parseDocs(t *testing.T, yaml string) []config.Document {
	t.Helper()
	docs, err := config.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

const diffNode = `
apiVersion: chalkos.dev/v1alpha1
kind: Node
metadata: {name: n1}
spec: {type: worker, labels: {zone: a, rack: r1}}
---
apiVersion: storage.chalkos.dev/v1alpha1
kind: Volume
metadata: {name: old}
spec: {mountPoint: /srv/old}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: token}
spec: {data: {token: s3cret, kept: same}}
`

const diffInventory = `
apiVersion: chalkos.dev/v1alpha1
kind: Node
metadata: {name: n1}
spec: {type: controlplane, labels: {zone: a, room: b}}
---
apiVersion: storage.chalkos.dev/v1alpha1
kind: Volume
metadata: {name: data}
spec: {mountPoint: /srv/data}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: token}
spec: {data: {token: n3w, kept: same}}
`

// TestDiffConfig compares a slot's documents, redacted as GetConfig returns them, with the
// inventory's: documents added, removed and changed, and the changed ones field by field, in order;
// a Secret's field shows only that it changed.
func TestDiffConfig(t *testing.T) {
	node := config.Redact(parseDocs(t, diffNode))
	changes := diffConfig(node, parseDocs(t, diffInventory))
	if got, want := changes.summary(), `config 1 added, 1 removed (Volume "old"), 2 changed`; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	var b bytes.Buffer
	changes.write(&b)
	want := `~ Node "n1"
    spec.labels.rack: "r1" → (none)
    spec.labels.room: (none) → "b"
    spec.type: "worker" → "controlplane"
~ Secret "token"
    spec.data.token: changed
+ Volume "data"
- Volume "old" (on the node, not in the inventory)
`
	if b.String() != want {
		t.Errorf("diff:\n%s\nwant:\n%s", b.String(), want)
	}
	if strings.Contains(b.String(), "s3cret") || strings.Contains(b.String(), "n3w") {
		t.Error("the diff shows a secret")
	}
	if got := diffConfig(node, parseDocs(t, diffNode)).summary(); got != "config unchanged" {
		t.Errorf("summary of the same config = %q", got)
	}
}

// TestDiffConfigOfLists compares lists by index, and a list or object against a value as a whole.
func TestDiffConfigOfLists(t *testing.T) {
	node := parseDocs(t, "apiVersion: a.example.org/v1\nkind: X\nmetadata: {name: x}\nspec: {list: [1, 2], obj: {a: 1}, empty: {}}\n")
	inv := parseDocs(t, "apiVersion: a.example.org/v1\nkind: X\nmetadata: {name: x}\nspec: {list: [1, 3, 4], obj: 5, empty: []}\n")
	var b bytes.Buffer
	diffConfig(node, inv).write(&b)
	want := `~ X "x"
    spec.empty: {} → []
    spec.list[1]: 2 → 3
    spec.list[2]: (none) → 4
    spec.obj: (none) → 5
    spec.obj.a: 1 → (none)
`
	if b.String() != want {
		t.Errorf("diff:\n%s\nwant:\n%s", b.String(), want)
	}
}
