package main

import (
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/layers"
)

// registry is the registry of the layers this chalkd serves, bound to node, for an image of the
// layers whose markers it carries.
func registry(node layer.Node, k *chalkd.Kubernetes, r *chalkd.Router) (*layer.Registry, error) {
	l, err := layers.Read(layers.Dir)
	if err != nil {
		return nil, err
	}
	return layer.NewRegistry(l, chalkd.NewKubernetesLayer(node, k), chalkd.NewRouterLayer(r))
}
