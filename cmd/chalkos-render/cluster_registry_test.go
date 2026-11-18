package main

import (
	"errors"
	"os"
	"testing"

	"github.com/trevex/chalkos/pkg/kubernetes/clusterregistry"
	"github.com/trevex/chalkos/pkg/refusals"
)

// clusterRegistryInput is the input of the output cluster-registry: a ClusterRegistry of the
// spec, none when it is nil, and the Network 10-lan of the addresses.
func clusterRegistryInput(t *testing.T, spec map[string]any, addresses ...any) string {
	t.Helper()
	docs := map[string]any{"Network": []any{doc("10-lan", map[string]any{"network": map[string]any{"address": addresses}})}}
	if spec != nil {
		docs["ClusterRegistry"] = []any{doc("cluster", spec)}
	}
	return kubernetesInput(t, docs)
}

// A node whose static address is an endpoint, spelled otherwise, gets the unit's port and its
// firewall table.
func TestClusterRegistryServing(t *testing.T) {
	input := clusterRegistryInput(t, map[string]any{"mirrors": []string{"docker.io"}, "endpoints": []string{"fd00::11"}, "port": 5001}, "192.0.2.11/24", "fd00:0::11/64")
	want := "# registry.env\n" + "PORT=5001\n" + "# registry.nft\n" + clusterregistry.Firewall(5001, "0x04000000")
	if got := renderedFiles(t, []string{"cluster-registry", "0x04000000"}, input); got != want {
		t.Errorf("rendered %s, want %s", got, want)
	}
}

// A node that serves no endpoint, and one without a ClusterRegistry, get no file, so the unit
// does not start.
func TestClusterRegistryElsewhere(t *testing.T) {
	for name, input := range map[string]string{
		"elsewhere": clusterRegistryInput(t, map[string]any{"mirrors": []string{"docker.io"}, "endpoints": []string{"fd00::11"}, "port": 5000}, "fd00::12/64"),
		"none":      clusterRegistryInput(t, nil, "fd00::11/64"),
	} {
		out, err := render(t, []string{"cluster-registry", "0x04000000"}, input)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if entries, _ := os.ReadDir(out); len(entries) != 0 {
			t.Errorf("%s: rendered %v", name, entries)
		}
	}
}

// A host a repository path cannot start with, which the schema lets through, is refused as the
// ClusterRegistry's.
func TestClusterRegistryRefuses(t *testing.T) {
	input := clusterRegistryInput(t, map[string]any{"mirrors": []string{"a.-b"}, "endpoints": []string{"fd00::11"}, "port": 5000}, "fd00::11/64")
	_, err := render(t, []string{"cluster-registry", "0x04000000"}, input)
	r, ok := errors.AsType[*refusals.Error](err)
	if !ok || r.Kind != clusterregistry.Kind || r.Name != "cluster" || r.Path != "spec" {
		t.Fatalf("refused with %v", err)
	}
}
