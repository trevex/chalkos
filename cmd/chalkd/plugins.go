package main

import (
	"github.com/trevex/chalkos/pkg/chalkd"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/layers"
)

// plugins are the layers compiled into this chalkd, bound to node; nil sides are chalkd's own.
func plugins(node layer.Node, k *chalkd.Kubernetes, r *chalkd.Router) []layer.Plugin {
	if k == nil {
		k = chalkd.NewKubernetes(chalkd.KubernetesOptions{})
	}
	if r == nil {
		r = &chalkd.Router{Run: node.Runner()}
	}
	return []layer.Plugin{chalkd.NewKubernetesLayer(node, k), chalkd.NewRouterLayer(r)}
}

// registry is the registry of the layers this chalkd serves, bound to node, for an image of the
// layers whose markers it carries.
func registry(node layer.Node, k *chalkd.Kubernetes, r *chalkd.Router) (*layer.Registry, error) {
	l, err := layers.Read(layers.Dir)
	if err != nil {
		return nil, err
	}
	return layer.NewRegistry(l, plugins(node, k, r)...)
}
