package check

import (
	"encoding/json"
	"fmt"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/identity"
)

// Translated is a config set the image accepts as a set: its documents with their defaults,
// sorted, and the identity they translate to.
type Translated struct {
	Documents config.Documents
	Identity  identity.Identity
}

// IdentityJSON is the identity as JSON, as a config slot keeps it.
func (t Translated) IdentityJSON() ([]byte, error) { return encodeIdentity(t.Identity) }

// Translate checks docs as the set of the node called node against the image's schemas, fills in
// their defaults and translates them into the node's identity on the image's target, as
// config.Translate does. A set the image does not accept is a *Refusal of StageConfig, whose
// message is the translation's error.
func (img Image) Translate(node string, docs []config.Document) (Translated, error) {
	set, id, err := config.Translate(node, docs, img.Schemas, img.Target)
	if err != nil {
		return Translated{}, &Refusal{Stage: StageConfig, Message: err.Error()}
	}
	return Translated{Documents: set.Documents(), Identity: id}, nil
}

// TranslateRedacted is Translate of redacted documents, as config.TranslateRedacted translates
// them: it returns the set's documents with their defaults, redacted.
func (img Image) TranslateRedacted(node string, docs config.Redacted) (config.Redacted, identity.Identity, error) {
	set, id, err := config.TranslateRedacted(node, docs, img.Schemas, img.Target)
	if err != nil {
		return config.Redacted{}, identity.Identity{}, &Refusal{Stage: StageConfig, Message: err.Error()}
	}
	return set, id, nil
}

// encodeIdentity is id as JSON.
func encodeIdentity(id identity.Identity) ([]byte, error) {
	data, err := json.Marshal(id)
	if err != nil {
		return nil, fmt.Errorf("encode the identity: %w", err)
	}
	return data, nil
}
