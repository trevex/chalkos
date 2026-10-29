package kubernetes

import (
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/inventory"
)

// kindOf is what the node is in Kubernetes: the type its Node document names, a control plane or
// a worker; "" without one, as for a node without Kubernetes or of a partial cluster.
func kindOf(c layer.Cluster, name string) (string, error) {
	docs, err := c.Documents(name)
	if err != nil {
		return "", err
	}
	return inventory.NodeType(docs), nil
}

// nodesOf lists the cluster's nodes of a Kubernetes kind but skip, in name order.
func nodesOf(c layer.Cluster, kind, skip string) ([]string, error) {
	var names []string
	for _, name := range c.NodeNames() {
		k, err := kindOf(c, name)
		if err != nil {
			return nil, err
		}
		if k == kind && name != skip {
			names = append(names, name)
		}
	}
	return names, nil
}
