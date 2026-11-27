package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	kuberneteslayer "github.com/trevex/chalkos/pkg/chalkctl/layers/kubernetes"
	routerlayer "github.com/trevex/chalkos/pkg/chalkctl/layers/router"
	"github.com/trevex/chalkos/pkg/sdk"
)

// plugins are chalkctl's layers, in the order of their names.
var plugins = []layer.Plugin{&kuberneteslayer.Plugin{}, &routerlayer.Plugin{}}

// compiled are the plugins that implement the hook T.
func compiled[T any]() []T {
	var list []T
	for _, p := range plugins {
		if h, ok := p.(T); ok {
			list = append(list, h)
		}
	}
	return list
}

// pluginsOf are the plugins of the layers a node's Info names that implement the hook T.
func pluginsOf[T any](names []string) []T {
	var list []T
	for _, p := range plugins {
		if h, ok := p.(T); ok && slices.Contains(names, p.Name()) {
			list = append(list, h)
		}
	}
	return list
}

// sdkLayers are the compiled plugins as the SDK takes them.
func sdkLayers() []sdk.Layer { return compiled[sdk.Layer]() }

// nodeCAHolders are the nodes that every compiled layer names as holders of the node CA, in the
// layers' order and each once.
func nodeCAHolders(c *cluster) ([]string, error) {
	var names []string
	for _, p := range compiled[layer.RotationNodes]() {
		holders, err := p.NodeCAHolders(c)
		if err != nil {
			return nil, err
		}
		for _, name := range holders {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	return names, nil
}

// listedCoordinator reports whether the inventory makes the node a coordinator of one of the
// layers chalkctl knows.
func listedCoordinator(c *cluster, name string) (bool, error) {
	listed := false
	for _, co := range compiled[layer.Coordinators]() {
		ok, err := co.ListedCoordinator(c, name)
		if err != nil {
			return false, err
		}
		listed = listed || ok
	}
	return listed, nil
}

// coordinatorsFirst is names with the coordinators first, each part in the order of names.
func coordinatorsFirst(names []string, coordinator func(name string) bool) []string {
	var first, rest []string
	for _, name := range names {
		if coordinator(name) {
			first = append(first, name)
		} else {
			rest = append(rest, name)
		}
	}
	return append(first, rest...)
}

// refreshRevoked has every compiled layer list those of names it lists as revoked as revoked now,
// through nodes, once a command run with --ignore-revoked handed them certificates or CA
// material, and writes what each refreshed to w. It joins the layers' errors, which say what to
// run.
func refreshRevoked(ctx context.Context, w io.Writer, nodes layer.Nodes, names []string) error {
	var errs []error
	for _, p := range compiled[layer.Revocations]() {
		lines, err := p.RefreshRevoked(ctx, nodes, names)
		for _, line := range lines {
			fmt.Fprintln(w, line)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
