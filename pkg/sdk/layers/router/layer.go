// Package router is the router layer's part in the SDK: the validator router, which it links.
package router

import (
	"slices"

	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/router/derive"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/sdk"
)

// Layer is the router layer.
type Layer struct{}

var (
	_ sdk.Layer      = Layer{}
	_ sdk.Validators = Layer{}
)

// Name is the router layer's.
func (Layer) Name() string { return layers.Router }

// Validators links the validator router for an image whose schemas hold the Router kind: the
// image runs it as chalkos-render validate router, a client runs derive.Validate in-process on
// the documents of the same kinds. An image without the router layer runs none.
func (Layer) Validators(s *schema.Schemas) []outputs.LinkedValidator {
	kinds := s.Kinds()
	if !slices.ContainsFunc(kinds, func(k schema.Kind) bool { return k.APIVersion == derive.API && k.Kind == "Router" }) {
		return nil
	}
	read := derive.Kinds()
	var inputs []string
	for _, k := range kinds {
		if slices.Contains(read, k.Kind) && !slices.Contains(inputs, k.Kind) {
			inputs = append(inputs, k.Kind)
		}
	}
	return []outputs.LinkedValidator{{Name: "router", Inputs: inputs, Validate: derive.Validate}}
}
