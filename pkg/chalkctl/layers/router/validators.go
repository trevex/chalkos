package router

import (
	"slices"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/router/derive"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/validators"
)

// Validators is the validator router of an image whose schemas hold the Router kind, as every
// router image's do. The image runs it as chalkos-render validate router on the documents of the
// kinds of derive.Kinds that it declares; chalkctl runs derive.Validate, the same code, in-process
// on the documents of the same kinds, so it refuses a config before the delivery as the node
// would. An image without the router layer runs none.
func (*Plugin) Validators(s *schema.Schemas) []validators.Linked {
	kinds := s.Kinds()
	if !slices.ContainsFunc(kinds, func(k schema.Kind) bool { return k.APIVersion == config.RouterAPI && k.Kind == "Router" }) {
		return nil
	}
	read := derive.Kinds()
	var inputs []string
	for _, k := range kinds {
		if slices.Contains(read, k.Kind) && !slices.Contains(inputs, k.Kind) {
			inputs = append(inputs, k.Kind)
		}
	}
	return []validators.Linked{{Name: "router", Inputs: inputs, Validate: derive.Validate}}
}
