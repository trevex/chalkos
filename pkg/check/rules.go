package check

import (
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/rules"
)

// Rules returns the change rules of set that next, a config set with its defaults, does not meet
// against old, the config it replaces. Rules see the node called node and its facts f. An error
// is a rule that could not be checked, or a fact that could not be loaded, which refuses the
// config as well.
func Rules(set *rules.Set, node string, next, old config.Redacted, f rules.Facts) ([]rules.Failure, error) {
	return set.Evaluate(rules.Input{Documents: next, Old: old, Node: node, Facts: f})
}
