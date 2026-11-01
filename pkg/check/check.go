// Package check checks a node's config as the image the config is for checks it, from what that
// image holds and what the node gives it, and changes nothing: it translates the documents, runs
// the image's change rules and renderers, and plans the effects a delivery of the config takes.
// It never sees a Secret's value. The documents arrive with their secret values hashed, as
// config.Redact hashes them, which is all rules and renderers read of them, and the checks of
// secret references need the Secrets' keys alone.
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
)

// Image is what an image checks config with, all of it its own: its schemas and the target its
// config translates for, its change rules, its effects, and its outputs with the renderer that
// runs their programs.
type Image struct {
	Schemas *schema.Schemas
	Target  config.Target
	Rules   *rules.Set
	Effects Effects
	Outputs map[string]outputs.Definition
	Render  outputs.Renderer
}

// Input is a config to check for the node called Node:
//   - Delivered are the documents as delivered, and Old the documents of the config they replace,
//     with their defaults; nil at an install. The secret values of both are hashed.
//   - Facts gives the node's facts for a check of the translated documents against Old, which
//     command facts read; it loads a fact when a rule first reads it. Run requires it.
//   - Compare are what the effects compare the config with: the config the node runs, or a
//     baseline and what the deliveries that failed since applied.
//   - Storage checks the config against the storage kinds' rules alone, as a config that passed
//     every check once is checked again for a change of the node's storage.
type Input struct {
	Node      string
	Delivered []config.Document
	Old       []config.Document
	Facts     func(next, old []config.Document) rules.Facts
	Compare   []Comparison
	Storage   bool
}

// Result is a config that passed: its documents with their defaults and their secret values
// hashed, the identity they translate to, its outputs, rendered and checked, and the plan of a
// delivery of it. Warnings name the effect rules that could not be evaluated, whose effects the
// plan takes all the same, as Due gives them. The caller discards or swaps in Staged.
type Result struct {
	Translated
	Staged   *outputs.Staged
	Plan     Plan
	Warnings []string
}

// Stage is the step of a check that refused a config.
type Stage string

const (
	// StageConfig is the translation: the documents against the schemas, the set's own rules,
	// and the identity they translate to.
	StageConfig Stage = "config"
	// StageRules is the image's change rules; a rule that cannot be evaluated refuses too.
	StageRules Stage = "rules"
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
// against in.Old and every output's renderer and check, and plans the effects. A refused config
// is a *Refusal; any other error is the image's own failure. A config refused after its
// translation still gives the translation, whose storage a caller names in the refusal.
//
// With in.Storage, only the rules of the storage kinds run, and nothing renders or plans: Staged
// is nil.
func Run(ctx context.Context, img Image, in Input) (Result, error) {
	if in.Facts == nil {
		return Result{}, errors.New("the check has no facts to give the rules")
	}
	tr, err := Translate(in.Node, in.Delivered, img.Schemas, img.Target)
	if err != nil {
		return Result{}, &Refusal{Stage: StageConfig, Message: ConfigRefusal(err)}
	}
	set := img.Rules
	if in.Storage {
		set = set.Of(config.StorageAPI)
	}
	failures, err := Rules(set, in.Node, tr.Documents, in.Old, in.Facts(tr.Documents, in.Old))
	if err != nil {
		return Result{Translated: tr}, &Refusal{Stage: StageRules, Message: err.Error()}
	}
	if len(failures) > 0 {
		return Result{Translated: tr}, &Refusal{Stage: StageRules, Message: rules.Refusal(failures, in.Node), Failures: failures}
	}
	if in.Storage {
		return Result{Translated: tr}, nil
	}
	staged, refused := img.Render.Render(ctx, img.Outputs, outputs.Input{Node: in.Node, Documents: tr.Documents, Schemas: img.Schemas, Hashed: true})
	if len(refused) > 0 {
		staged.Discard()
		return Result{Translated: tr}, &Refusal{Stage: StageOutputs, Message: outputs.Refusal(refused)}
	}
	o, warnings, err := Due(img.Effects, Delivery{Node: in.Node, Documents: tr.Documents, Schemas: img.Schemas, Outputs: staged.States()}, in.Compare)
	if err != nil {
		staged.Discard()
		return Result{}, err
	}
	return Result{Translated: tr, Staged: staged, Plan: PlanOf(img.Effects, o), Warnings: warnings}, nil
}
