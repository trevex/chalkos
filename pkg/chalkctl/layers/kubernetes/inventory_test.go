package kubernetes

import (
	"strings"
	"testing"

	k8s "github.com/trevex/chalkos/pkg/kubernetes"
)

// TestCheckInventorySkipsUnreadableNodes passes a cluster with a node whose config the inventory
// cannot read, which the commands that read it refuse, yet refuses two readable nodes whose
// Cluster specs differ.
func TestCheckInventorySkipsUnreadableNodes(t *testing.T) {
	types := map[string]string{"cp1": k8s.KindControlPlane, "w1": k8s.KindWorker, "w2": k8s.KindWorker}
	c := fakeCluster{types: types, apiServer: map[string]string{"cp1": "a", "w1": "a"}, broken: map[string]bool{"w2": true}}
	if err := (&Plugin{}).CheckInventory(c); err != nil {
		t.Errorf("CheckInventory = %v, want the broken node skipped", err)
	}
	c.apiServer["w1"] = "b"
	if err := (&Plugin{}).CheckInventory(c); err == nil || !strings.Contains(err.Error(), "cp1") || !strings.Contains(err.Error(), "w1") {
		t.Errorf("CheckInventory = %v, want cp1 and w1's Cluster specs refused", err)
	}
}
