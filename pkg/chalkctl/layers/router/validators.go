package router

import (
	"slices"

	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/router/derive"
	"github.com/trevex/chalkos/pkg/schema"
)

// Validators links the validator router for an image whose schemas hold the Router kind: the
// image runs it as chalkos-render validate router, chalkctl runs derive.Validate in-process on the
// documents of the same kinds.
func (*Plugin) Validators(s *schema.Schemas) []outputs.LinkedValidator {
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
