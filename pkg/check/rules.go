package check

import (
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/rules"
)

// Rules returns the change rules of set that next, a config set with its defaults, does not meet
// against old, the config it replaces; both have their secret values hashed. Rules see the node
// called node and its facts f. An error is a rule that could not be checked, or a fact that could
// not be loaded, which refuses the config as well.
func Rules(set *rules.Set, node string, next, old []config.Document, f rules.Facts) ([]rules.Failure, error) {
	return set.Evaluate(rules.Input{Documents: next, Hashed: true, Old: old, OldHashed: true, Node: node, Facts: f})
}
