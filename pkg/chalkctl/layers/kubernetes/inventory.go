package kubernetes

import (
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/internal/strs"
	"github.com/trevex/chalkos/pkg/kubernetes/translate"
)

// NodeDocuments leaves the Addon documents to the control planes.
func (*Plugin) NodeDocuments(docs []config.Document) []config.Document {
	return translate.NodeDocuments(docs)
}

// CheckInventory refuses nodes that hold different Cluster, Flannel or ClusterRegistry specs, or a
// node that holds two documents of one of these kinds.
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

// InventoryWarnings warn when the nodes run flannel and take its VXLAN from any source: flannel's
// VXLAN is not authenticated, so any host that reaches a node's address can send it pod traffic
// from a forged pod address. The warning suggests the prefixes of the nodes' addresses.
func (*Plugin) InventoryWarnings(c layer.Cluster) []string {
	open, suggested := translate.OpenVXLAN(c.NodeNames(), c.Documents)
	if !open {
		return nil
	}
	fix := "set it to the networks of the nodes' addresses"
	if len(suggested) > 0 {
		fix = "set it to the networks of the nodes' addresses, such as vxlanSourceSubnets: [" + strs.Join(suggested, ", ") + "], which covers the addresses the inventory gives"
	}
	return []string{"Flannel: vxlanSourceSubnets is empty, so every node takes flannel's VXLAN from any source, and any host that reaches a node can send it pod traffic from forged pod addresses; " + fix}
}
