package addons

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/kubernetes"
)

// decode decodes data, which must hold a single JSON value, keeping numbers as json.Number so
// that they compare exactly.
func decode(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("data after the JSON value")
	}
	return v, nil
}

// normalized is v as JSON decodes it, with strings that hold a JSON object decoded too, as
// ConfigMaps carry configuration files: key order and spacing do not count.
func normalized(t *testing.T, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out, err := decode(data)
	if err != nil {
		t.Fatal(err)
	}
	return decodeStrings(out)
}

func decodeStrings(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = decodeStrings(e)
		}
	case []any:
		for i, e := range x {
			x[i] = decodeStrings(e)
		}
	case string:
		if strings.HasPrefix(x, "{") {
			if o, err := decode([]byte(x)); err == nil {
				return decodeStrings(o)
			}
		}
	}
	return v
}

// TestAddonsMatchNix renders the addons of every Kubernetes image of every manifest the
// repository has, which CHALKOS_TEST_BRIDGE holds with the objects Nix renders for it, and
// requires the same objects in the same order.
func TestAddonsMatchNix(t *testing.T) {
	root := os.Getenv("CHALKOS_TEST_BRIDGE")
	if root == "" {
		t.Skip("CHALKOS_TEST_BRIDGE is not set; the go-unit check sets it")
	}
	contracts, err := filepath.Glob(filepath.Join(root, "*", "contracts", "*"))
	if err != nil {
		t.Fatal(err)
	}
	compared := 0
	for _, dir := range contracts {
		var want []json.RawMessage
		data, err := os.ReadFile(filepath.Join(dir, "addons.json"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &want); err != nil {
			t.Fatal(err)
		}
		name, _ := filepath.Rel(root, dir)
		t.Run(name, func(t *testing.T) {
			c, err := kubernetes.ReadCluster(filepath.Join(dir, "kubernetes.json"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := Render(c)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want) {
				t.Errorf("%d objects, want %d", len(got), len(want))
			}
			for i := range min(len(got), len(want)) {
				w, g := normalized(t, want[i]), normalized(t, got[i])
				if !reflect.DeepEqual(w, g) {
					wj, _ := json.MarshalIndent(w, "", "  ")
					gj, _ := json.MarshalIndent(g, "", "  ")
					t.Errorf("object %d:\n%s\nwant\n%s", i, gj, wj)
				}
			}
		})
		compared++
	}
	if compared == 0 {
		t.Fatalf("%s holds no Kubernetes image", root)
	}
}
