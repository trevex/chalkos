package main

import (
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes"
	"github.com/trevex/chalkos/pkg/chalkd/layers/router"
	"github.com/trevex/chalkos/pkg/layers"
)

// plugins are the layers compiled into this chalkd, bound to node.
func plugins(node layer.Node) []layer.Plugin {
	return []layer.Plugin{kubernetes.New(node, kubernetes.Options{}), router.New(node, router.Options{})}
}

// registry is the registry of the layers this chalkd serves, bound to node, for an image of the
// layers whose markers it carries.
func registry(node layer.Node) (*layer.Registry, error) {
	l, err := layers.Read(layers.Dir)
	if err != nil {
		return nil, err
	}
	return layer.NewRegistry(l, plugins(node)...)
}
