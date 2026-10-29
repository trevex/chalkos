package kubernetes

import (
	"slices"
	"testing"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/config"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
)

// fakeCluster is a cluster of nodes with the Node types given; "" for a node without one.
type fakeCluster struct {
	layer.Cluster
	types map[string]string
}

func (c fakeCluster) NodeNames() []string {
	var names []string
	for name := range c.types {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (c fakeCluster) Documents(name string) ([]config.Document, error) {
	docs := []config.Document{{APIVersion: config.CoreAPI, Kind: "Host", Object: map[string]any{"spec": map[string]any{}}}}
	if kind := c.types[name]; kind != "" {
		docs = append(docs, config.Document{APIVersion: config.KubernetesAPI, Kind: "Node", Name: "default", Object: map[string]any{"spec": map[string]any{"type": kind}}})
	}
	return docs, nil
}

// TestNodesOf lists nodes by the type their Node documents give them.
func TestNodesOf(t *testing.T) {
	c := fakeCluster{types: map[string]string{"cp3": k8s.KindControlPlane, "cp1": k8s.KindControlPlane, "w1": k8s.KindWorker, "n1": ""}}
	if got, err := nodesOf(c, k8s.KindControlPlane, "cp3"); err != nil || !slices.Equal(got, []string{"cp1"}) {
		t.Errorf("control planes but cp3 = %q, %v", got, err)
	}
	if got, err := nodesOf(c, "", ""); err != nil || !slices.Equal(got, []string{"n1"}) {
		t.Errorf("nodes without Kubernetes = %q, %v", got, err)
	}
	if kind, err := kindOf(c, "w1"); err != nil || kind != k8s.KindWorker {
		t.Errorf("kind of w1 = %q, %v", kind, err)
	}
}
