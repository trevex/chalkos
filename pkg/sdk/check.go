package sdk

import (
	"context"
	"slices"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/constants"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/rules"
)

// Delivery is a node's config checked against its image, with the identity it translates to and
// its redacted documents with defaults, as the image's rules see them.
type Delivery struct {
	config.Encoded
	Identity identity.Identity
	// Redacted are the translated documents, redacted, with the image's defaults.
	Redacted config.Redacted
	// Unpinned names the image references that have a tag but no digest, which a client warns
	// about.
	Unpinned []string
}

// Check checks the node's config documents against the contract as the node would, but without
// the node's current config: rules of fields that read oldSelf do not run and rules of kinds see
// no oldDocuments. It returns the config ready to deliver; a refusal is a *check.Refusal.
func Check(ctx context.Context, c Contract, node string, docs []config.Document) (Delivery, error) {
	img, err := c.Checker()
	if err != nil {
		return Delivery{}, err
	}
	tr, err := img.Translate(node, docs)
	if err != nil {
		return Delivery{}, err
	}
	redacted := config.Redact(tr.Documents)
	if err := img.Rules(rules.Input{Documents: redacted, Node: node}); err != nil {
		return Delivery{}, err
	}
	if err := img.Validate(ctx, node, redacted); err != nil {
		return Delivery{}, err
	}
	e, err := config.Encode(docs)
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{Encoded: e, Identity: tr.Identity, Redacted: redacted, Unpinned: tr.Documents.UnpinnedImages(c.Image.Schemas)}, nil
}

// CheckRules runs the change rules that read no facts on next against old, the config the node
// serves (empty when unknown, so oldSelf rules do not run). old gets the image's defaults; node is
// the node's name, which rules read as node.name.
func CheckRules(c Contract, next, old config.Redacted, node string) error {
	img, err := c.Checker()
	if err != nil {
		return err
	}
	return img.Rules(rules.Input{Documents: next, Old: old.Defaulted(img.Schemas), Node: node})
}

// Refused reports whether err is a refusal of config by an image that stands until the config or
// the image changes, so a caller that retries stops: a lasting *check.Refusal of Check or the
// other local checks, or a node's refusal, either with the reason CONFIG_REFUSED or with a reason
// a change rule may name, one of constants.RuleReasons such as NO_FALLBACK_SECRET; client.Reason
// tells which. A refusal because a rule could not be evaluated, a fact could not be loaded or a
// program failed without saying what it refuses carries no reason and is not Refused: it may pass
// next time.
func Refused(err error) bool {
	reason := client.Reason(err)
	return reason == nodev1.ErrorReason_ERROR_REASON_CONFIG_REFUSED || slices.Contains(constants.RuleReasons, client.ReasonName(reason))
}
