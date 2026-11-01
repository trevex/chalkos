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
	"strings"

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
//   - Facts gives the node's facts, which it loads when a rule first reads one.
//   - Compare are what the effects compare the config with: the config the node runs, or a
//     baseline and what the deliveries that failed since applied.
type Input struct {
	Node      string
	Delivered []config.Document
	Old       []config.Document
	Facts     rules.Facts
	Compare   []Comparison
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
// is a *Refusal; any other error is the image's own failure.
func Run(ctx context.Context, img Image, in Input) (Result, error) {
	tr, err := Translate(in.Node, in.Delivered, img.Schemas, img.Target)
	if err != nil {
		return Result{}, &Refusal{Stage: StageConfig, Message: "the config: " + err.Error()}
	}
	failures, err := Rules(img.Rules, in.Node, tr.Documents, in.Old, in.Facts)
	if err != nil {
		return Result{}, &Refusal{Stage: StageRules, Message: err.Error()}
	}
	if len(failures) > 0 {
		return Result{}, &Refusal{Stage: StageRules, Message: rules.Refusal(failures, in.Node), Failures: failures}
	}
	staged, refused := img.Render.Render(ctx, img.Outputs, outputs.Input{Node: in.Node, Documents: tr.Documents, Schemas: img.Schemas, Hashed: true})
	if len(refused) > 0 {
		staged.Discard()
		msgs := make([]string, len(refused))
		for i, f := range refused {
			msgs[i] = f.Error()
		}
		return Result{}, &Refusal{Stage: StageOutputs, Message: strings.Join(msgs, "; ")}
	}
	o, warnings, err := Due(img.Effects, Delivery{Node: in.Node, Documents: tr.Documents, Schemas: img.Schemas, Outputs: staged.States()}, in.Compare)
	if err != nil {
		staged.Discard()
		return Result{}, err
	}
	return Result{Translated: tr, Staged: staged, Plan: PlanOf(img.Effects, o), Warnings: warnings}, nil
}
