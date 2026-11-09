// Package chalkdtest holds the fixtures of the tests that run chalkd's server: the layers chalkd
// compiles in and the default image's effects. It does not import chalkd, so chalkd's own tests
// share it.
package chalkdtest

import (
	"encoding/json"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/chalkd/layer"
	kubernetesplugin "github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes"
	routerplugin "github.com/trevex/chalkos/pkg/chalkd/layers/router"
	"github.com/trevex/chalkos/pkg/effects"
	"github.com/trevex/chalkos/pkg/layers"
)

// EffectsFixture is the default image's effects.json, from a package two directories below the
// repository root, such as pkg/chalkd.
const EffectsFixture = "../../test/fixtures/kinds/effects.json"

// Plugins compiles the Kubernetes and router layers into node, on an image of the layers l: the
// plugins given, and for a layer none of them is, one built with chalkd's own options.
func Plugins(t testing.TB, node layer.Node, l layers.Set, plugins ...layer.Plugin) *layer.Registry {
	t.Helper()
	given := func(name string) bool {
		return slices.ContainsFunc(plugins, func(p layer.Plugin) bool { return p.Name() == name })
	}
	if !given(layers.Kubernetes) {
		plugins = append(plugins, kubernetesplugin.New(node, kubernetesplugin.Options{}))
	}
	if !given(layers.Router) {
		plugins = append(plugins, routerplugin.New(node, routerplugin.Options{}))
	}
	reg, err := layer.NewRegistry(l, plugins...)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// Effects are the default image's effects by name, without those that run an action of a layer
// an image of the layers l does not carry, as an image without the layer has none. An action
// that one of l lacks stays, so the delivery refuses it.
func Effects(t testing.TB, l layers.Set) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(EffectsFixture)
	if err != nil {
		t.Fatal(err)
	}
	var defs map[string]json.RawMessage
	if err := json.Unmarshal(data, &defs); err != nil {
		t.Fatal(err)
	}
	maps.DeleteFunc(defs, func(_ string, raw json.RawMessage) bool {
		var d effects.Definition
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		name, _, _ := strings.Cut(d.Chalkd, ":")
		return d.Chalkd != "" && !l.Has(name)
	})
	return defs
}
