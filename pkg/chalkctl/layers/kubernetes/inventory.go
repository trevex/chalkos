package kubernetes

import (
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/kubernetes/translate"
)

var _ layer.Inventory = (*Plugin)(nil)

// NodeDocuments leaves the Addon documents to the control planes.
func (*Plugin) NodeDocuments(docs []config.Document) []config.Document {
	return translate.NodeDocuments(docs)
}

// CheckInventory refuses nodes that hold different Cluster or Flannel specs, or a node that holds
// two Cluster or two Flannel documents.
func (*Plugin) CheckInventory(c layer.Cluster) error {
	_, err := endpoint(c)
	return err
}

// endpoint checks the nodes' Cluster and Flannel specs and returns the URL of the cluster's
// Kubernetes API server, which their Cluster documents name; "" without one. A node whose config
// the inventory cannot read is skipped: each command that reads its config refuses it, and the
// others still reach it.
func endpoint(c layer.Cluster) (string, error) {
	return translate.Endpoint(c.NodeNames(), func(name string) ([]config.Document, error) {
		docs, err := c.Documents(name)
		if err != nil {
			return nil, nil
		}
		return docs, nil
	})
}
