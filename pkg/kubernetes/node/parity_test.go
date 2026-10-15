package node

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/trevex/chalkos/pkg/kubernetes"
)

// decodeNumbers decodes data keeping numbers as json.Number, so that they compare exactly.
func decodeNumbers(t *testing.T, data []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestKubeletConfigMatchesNix renders the kubelet's configuration of every Kubernetes image of
// every manifest the repository has, which CHALKOS_TEST_BRIDGE holds with Nix's rendering of it.
func TestKubeletConfigMatchesNix(t *testing.T) {
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
		data, err := os.ReadFile(filepath.Join(dir, "kubelet.json"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		c, err := kubernetes.ReadCluster(filepath.Join(dir, "kubernetes.json"))
		if err != nil {
			t.Fatal(err)
		}
		gotData, err := json.Marshal(KubeletConfig(DefaultPaths(), c))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := decodeNumbers(t, gotData), decodeNumbers(t, data); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: kubelet config =\n%s\nwant\n%s", dir, gotData, data)
		}
		compared++
	}
	if compared == 0 {
		t.Fatalf("%s holds no Kubernetes image", root)
	}
}
