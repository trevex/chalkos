package check

import (
	"encoding/json"
	"fmt"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

// Translated is a config set the image accepts as a set: its documents with their defaults,
// sorted, and the identity they translate to (JSON).
type Translated struct {
	Documents config.Documents
	Identity  []byte
}

// Translate checks docs as the set of the node called node against schemas, fills in their
// defaults and translates them into the node's identity on the image t describes, as
// config.Translate does. docs may hold their secret values or their hashes: the checks of secret
// references need the Secrets' keys alone, and the documents keep what they hold.
func Translate(node string, docs []config.Document, schemas *schema.Schemas, t config.Target) (Translated, error) {
	set, id, err := config.Translate(node, docs, schemas, t)
	if err != nil {
		return Translated{}, err
	}
	data, err := json.Marshal(id)
	if err != nil {
		return Translated{}, fmt.Errorf("encode the identity: %w", err)
	}
	return Translated{Documents: set.Documents(), Identity: data}, nil
}

// Rules returns the change rules of set that next, a config set with its defaults, does not meet
// against old, the config it replaces; both have their secret values hashed. Rules see the node
// called node and its facts f. An error is a rule that could not be checked, or a fact that could
// not be loaded, which refuses the config as well.
func Rules(set *rules.Set, node string, next, old []config.Document, f rules.Facts) ([]rules.Failure, error) {
	return set.Evaluate(rules.Input{Documents: next, Hashed: true, Old: old, OldHashed: true, Node: node, Facts: f})
}
