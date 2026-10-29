package chalkctl

import (
	"slices"
	"time"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	kuberneteslayer "github.com/trevex/chalkos/pkg/chalkctl/layers/kubernetes"
	routerlayer "github.com/trevex/chalkos/pkg/chalkctl/layers/router"
)

// plugins are chalkctl's layers, in the order of their names.
var plugins = []layer.Plugin{kuberneteslayer.New(), routerlayer.New()}

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

// payloadsOf are the payloads every compiled layer issues t at now.
func payloadsOf(t *target, now time.Time) ([]*anypb.Any, error) {
	var payloads []*anypb.Any
	for _, d := range compiled[layer.Delivery]() {
		p, err := d.Payloads(pluginTarget{t}, now)
		if err != nil {
			return nil, err
		}
		payloads = append(payloads, p...)
	}
	return payloads, nil
}
