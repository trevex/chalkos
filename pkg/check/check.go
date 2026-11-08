// Package check checks a node's config as the image the config is for checks it, from what that
// image holds and what the node gives it, and changes nothing: it translates the documents, runs
// the image's change rules, validators and renderers, and plans the effects a delivery of the
// config takes. It never sees a Secret's value: it takes the documents as config.Redacted, which
// is all rules, validators and renderers read of them, and the checks of secret references need
// the Secrets' keys alone.
//
// chalkd runs these steps for a delivery to the running image, with its own checks of the machine
// between them; Run runs them in a row for an image that checks its own config.
package check

import (
	"context"
	"errors"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/validators"
)

// Image is what an image checks config with, all of it its own: its schemas and the target its
// config translates for, its change rules, its effects, its validators, and its outputs with the
// renderer that runs their programs and the validators'.
type Image struct {
	Schemas    *schema.Schemas
	Target     config.Target
	Rules      *rules.Set
	Effects    Effects
	Validators map[string]validators.Definition
	Outputs    map[string]outputs.Definition
	Render     outputs.Renderer
}

// Input is a config to check for the node called Node:
//   - Delivered are the documents as delivered, and Old the documents of the config they replace,
//     as delivered too, or with the defaults of the image that checks; empty at an install.
//   - Facts gives the node's facts for a check of the translated documents against Old, with its
//     defaults, which command facts read; it loads a fact when a rule first reads it. Run requires
//     it.
//   - Compare are what the effects compare the config with: the config the node runs, or a
//     baseline and what the deliveries that failed since applied.
type Input struct {
	Node      string
	Delivered config.Redacted
	Old       config.Redacted
	Facts     func(next, old config.Redacted) rules.Facts
	Compare   []Comparison
}

// Result is a config that passed: its documents with their defaults, redacted, the identity they
// translate to (JSON), its outputs, rendered and checked, and the plan of a delivery of it. Warnings name the effect rules that could not be evaluated, whose effects the
// plan takes all the same, as Due gives them. The caller discards or swaps in Staged.
type Result struct {
	Documents config.Redacted
	Identity  []byte
	Staged    *outputs.Staged
	Plan      Plan
	Warnings  []string
}

// Stage is the step of a check that refused a config.
type Stage string

const (
	// StageConfig is the translation: the documents against the schemas, the set's own rules,
	// and the identity they translate to.
	StageConfig Stage = "config"
	// StageRules is the image's change rules; a rule that cannot be evaluated refuses too.
	StageRules Stage = "rules"
	// StageValidators is the image's validators; one that fails refuses too.
	StageValidators Stage = "validators"
	// StageOutputs is the image's renderers and their checks.
	StageOutputs Stage = "outputs"
)

// Refusal is a config a check refused: the stage that refused it, and why, as a delivery's
// refusal says it. Failures are the change rules the config does not meet, for StageRules.
type Refusal struct {
	Stage    Stage
	Message  string
	Failures []rules.Failure
}

func (r *Refusal) Error() string { return r.Message }

// Run checks in as img checks it: it translates in.Delivered, runs the image's change rules
// against in.Old with the image's defaults, its validators, and every output's renderer and
// check, and plans the effects. A refused config is a *Refusal; any other error is the image's
// own failure. A config refused after its translation still gives the translation, whose storage
// a caller names in the refusal.
func Run(ctx context.Context, img Image, in Input) (Result, error) {
	if in.Facts == nil {
		return Result{}, errors.New("the check has no facts to give the rules")
	}
	docs, id, err := config.TranslateRedacted(in.Node, in.Delivered, img.Schemas, img.Target)
	if err != nil {
		return Result{}, &Refusal{Stage: StageConfig, Message: ConfigRefusal(err)}
	}
	res := Result{Documents: docs}
	if res.Identity, err = encodeIdentity(id); err != nil {
		return Result{}, err
	}
	old := in.Old.Defaulted(img.Schemas)
	failures, err := Rules(img.Rules, in.Node, docs, old, in.Facts(docs, old))
	if err != nil {
		return res, &Refusal{Stage: StageRules, Message: err.Error()}
	}
	if len(failures) > 0 {
		return res, &Refusal{Stage: StageRules, Message: rules.Refusal(failures, in.Node), Failures: failures}
	}
	input := outputs.Input{Node: in.Node, Documents: docs, Schemas: img.Schemas}
	rs, failed, err := validators.Run(ctx, img.Render, img.Validators, input)
	if err != nil {
		return Result{}, err
	}
	if len(rs) > 0 || len(failed) > 0 {
		return res, &Refusal{Stage: StageValidators, Message: validators.Refusal(rs, failed)}
	}
	staged, refused := img.Render.Render(ctx, img.Outputs, input)
	if len(refused) > 0 {
		staged.Discard()
		return res, &Refusal{Stage: StageOutputs, Message: outputs.Refusal(refused)}
	}
	o, warnings, err := Due(img.Effects, Delivery{Node: in.Node, Documents: docs, Schemas: img.Schemas, Outputs: staged.States()}, in.Compare)
	if err != nil {
		staged.Discard()
		return Result{}, err
	}
	res.Staged, res.Plan, res.Warnings = staged, PlanOf(img.Effects, o), warnings
	return res, nil
}
