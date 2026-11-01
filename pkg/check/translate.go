package check

import (
	"encoding/json"
	"fmt"

	"github.com/trevex/chalkos/pkg/config"
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
// config.Translate does. docs may hold their secret values or their hashes, as Redact gives them,
// with the same result: the checks of secret references need the Secrets' keys alone, a hashed
// value keeps the JSON type of its value for the schemas, and the documents keep what they hold.
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

// ConfigRefusal is the refusal of a config set that Translate refuses with err, as a delivery and
// Run give it.
func ConfigRefusal(err error) string {
	return "the config: " + err.Error()
}
