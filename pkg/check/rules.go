package check

import (
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

// Rules returns the change rules of set that next, a config set with its defaults, does not meet
// against old, the config it replaces; both have their secret values hashed. Rules see the node
// called node and its facts f. An error is a rule that could not be checked, or a fact that could
// not be loaded, which refuses the config as well.
func Rules(set *rules.Set, node string, next, old []config.Document, f rules.Facts) ([]rules.Failure, error) {
	return set.Evaluate(rules.Input{Documents: next, Hashed: true, Old: old, OldHashed: true, Node: node, Facts: f})
}

// Defaulted is old, the documents of a config as delivered, with the defaults schemas give them
// filled in, as the rules of the image whose schemas they are compare a config with them. The
// documents another image delivered or translated mean to this image what its own defaults make
// of them: a field the delivery left out has this image's default, not one another image gave
// it. A document of a kind schemas does not accept is left out, as no rule of this image reads
// its kind. Documents with defaults already keep them.
func Defaulted(schemas *schema.Schemas, old []config.Document) []config.Document {
	var out []config.Document
	for _, d := range old {
		filled, err := schemas.Default(d.Object)
		if err != nil {
			continue
		}
		d.Object = filled
		out = append(out, d)
	}
	return out
}
