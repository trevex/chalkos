// Package check checks a node's config as the image the config is for checks it, and changes
// nothing. A check has four stages, each a method of Image, in this order:
//
//  1. Translate: the documents against the schemas, the set's own rules and the node's identity;
//  2. Rules: the image's change rules, against the config the new one replaces;
//  3. Validate: the image's validators;
//  4. Render: the image's renderers and their checks.
//
// Each stage returns a *Refusal for a config it refuses and any other error for a failure of the
// image or the check. A check never sees a Secret's value: rules, validators and renderers read
// config.Redacted, and the checks of secret references need the Secrets' keys alone.
package check

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

// Image is what an image checks config with, all of it its own: its schemas and the target its
// config translates for, its change rules, its effects, its validators, and its outputs with the
// renderer that runs their programs. A stage whose part is nil checks nothing.
type Image struct {
	Schemas     *schema.Schemas
	Target      config.Target
	ChangeRules *rules.Set
	Effects     Effects
	Validators  Validators
	Outputs     map[string]outputs.Definition
	Renderer    outputs.Renderer
}

// Validators are the validators an image runs on a config: Programs on a node, Linked where only
// those a program links can run.
type Validators interface {
	// Validate runs each validator on in and returns the failures of those that refused the
	// config or did not run to their end. An error is the runner's own, and no validator's.
	Validate(ctx context.Context, in outputs.Input) ([]outputs.Failure, error)
}

// Programs are the validators an image declares, by name, whose programs Render runs.
type Programs struct {
	Defs   map[string]outputs.Validator
	Render outputs.Renderer
}

// Validate runs the programs of p.
func (p Programs) Validate(ctx context.Context, in outputs.Input) ([]outputs.Failure, error) {
	return p.Render.Validate(ctx, p.Defs, in)
}

// Linked are validators linked into the program that checks, which run in-process.
type Linked []outputs.LinkedValidator

// Validate runs l in the order given; it never fails itself.
func (l Linked) Validate(_ context.Context, in outputs.Input) ([]outputs.Failure, error) {
	return outputs.RunLinked(l, in), nil
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

// Refusal is a config a check refused: the stage that refused it and why. Failures holds the
// change rules not met (StageRules), Failed the validators or outputs that failed. Reason is
// the reason the refusal carries to the client: the reason of the first failed rule that names
// one, or, for a refusal an image's checker reported, the reason ErrorReason gave there.
type Refusal struct {
	Stage    Stage
	Message  string
	Failures []rules.Failure
	Failed   []outputs.Failure
	Reason   string
}

func (r *Refusal) Error() string { return r.Message }

// RefusedReason is the reason of a lasting refusal whose rules name none: CONFIG_REFUSED, the
// node API's ErrorReason of a config an image refuses, without its ERROR_REASON_ prefix.
const RefusedReason = "CONFIG_REFUSED"

// Lasting reports whether the image refuses the config itself, so the refusal stands until the
// config or the image changes: a set the translation refuses, change rules the config does not
// meet, or a validator or output that reports refusals of the documents. A rule that cannot be
// evaluated, a fact that cannot be loaded or a program that fails without saying what it refuses
// is not lasting.
func (r *Refusal) Lasting() bool {
	return r.Stage == StageConfig || len(r.Failures) > 0 ||
		slices.ContainsFunc(r.Failed, func(f outputs.Failure) bool { return len(f.Refusals) > 0 })
}

// ErrorReason is the reason the refusal carries to the client: Reason when it has one, as
// constants.RuleReasons names a rule's, RefusedReason for another lasting refusal, and none
// otherwise.
func (r *Refusal) ErrorReason() string {
	switch {
	case r.Reason != "":
		return r.Reason
	case r.Lasting():
		return RefusedReason
	}
	return ""
}

// Rules checks in.Documents against the image's change rules, which compare it with in.Old. A rule
// that cannot be checked, or reads a fact that cannot be loaded, refuses the config too. Without
// in.Facts, the rules that read facts do not run.
func (img Image) Rules(in rules.Input) error {
	if img.ChangeRules == nil {
		return nil
	}
	failures, err := img.ChangeRules.Evaluate(in)
	if err != nil {
		return &Refusal{Stage: StageRules, Message: err.Error()}
	}
	if len(failures) > 0 {
		r := &Refusal{Stage: StageRules, Message: rules.Refusal(failures, in.Node), Failures: failures}
		if i := slices.IndexFunc(failures, func(f rules.Failure) bool { return f.Reason != "" }); i >= 0 {
			r.Reason = failures[i].Reason
		}
		return r
	}
	return nil
}

// Validate runs the image's validators on docs, a config set with its defaults, for the node
// called node. A validator that refuses the config or fails refuses it.
func (img Image) Validate(ctx context.Context, node string, docs config.Redacted) error {
	if img.Validators == nil {
		return nil
	}
	failed, err := img.Validators.Validate(ctx, img.input(node, docs))
	if err != nil {
		return fmt.Errorf("the image's validators: %w", err)
	}
	if len(failed) > 0 {
		return &Refusal{Stage: StageValidators, Message: outputs.Message(failed), Failed: failed}
	}
	return nil
}

// Render renders, checks and stages every output of the image from docs; it changes nothing a
// service reads. With refuse, a failed output discards everything staged; without it, Render
// returns the outputs that passed along with the refusal, so a delivery can go ahead in part.
func (img Image) Render(ctx context.Context, node string, docs config.Redacted, refuse bool) (*outputs.Staged, error) {
	staged, failed := img.Renderer.Render(ctx, img.Outputs, img.input(node, docs))
	if len(failed) == 0 {
		return staged, nil
	}
	err := &Refusal{Stage: StageOutputs, Message: outputs.Message(failed), Failed: failed}
	if refuse {
		staged.Discard()
		return nil, err
	}
	return staged, err
}

// input is what the image's programs read of docs for the node called node.
func (img Image) input(node string, docs config.Redacted) outputs.Input {
	return outputs.Input{Node: node, Documents: docs, Schemas: img.Schemas}
}

// Input is a config to check for the node called Node. Old is the config Delivered replaces, as
// delivered, or with the defaults of the image that checks; empty at an install. Facts, which Run
// requires, loads each fact when a rule first reads it. Compare holds what the effects compare the
// config with: the config the node runs, or a baseline and what the deliveries that failed since
// applied; none plans no effects.
type Input struct {
	Node      string
	Delivered config.Redacted
	Old       config.Redacted
	Facts     func(next, old config.Redacted) rules.Facts
	Compare   []Comparison
}

// Result is a config that passed: its documents with defaults, its identity as JSON, its staged
// outputs and the plan of its delivery. Warnings name effect rules that could not be evaluated,
// whose effects the plan takes all the same. The caller discards or swaps in Staged.
type Result struct {
	Documents config.Redacted
	Identity  []byte
	Staged    *outputs.Staged
	Plan      Plan
	Warnings  []string
}

// errNoFacts is the error of a check whose input gives no facts.
var errNoFacts = errors.New("the check has no facts to give the rules")

// Run runs every stage on in.Delivered and plans the effects. A refused config is a *Refusal. A
// config refused after its translation still returns the translation, so a caller can name it.
func Run(ctx context.Context, img Image, in Input) (Result, error) {
	if in.Facts == nil {
		return Result{}, errNoFacts
	}
	docs, id, err := img.TranslateRedacted(in.Node, in.Delivered)
	if err != nil {
		return Result{}, err
	}
	identity, err := encodeIdentity(id)
	if err != nil {
		return Result{}, err
	}
	res, err := RunTranslated(ctx, img, in, docs)
	res.Identity = identity
	return res, err
}

// RunTranslated is Run after the translation, on docs in place of in.Delivered; the result holds
// no identity. It serves documents checked as a set by Image.CheckRedacted, as an image's tests do.
func RunTranslated(ctx context.Context, img Image, in Input, docs config.Redacted) (Result, error) {
	if in.Facts == nil {
		return Result{}, errNoFacts
	}
	res := Result{Documents: docs}
	old := in.Old.Defaulted(img.Schemas)
	if err := img.Rules(rules.Input{Documents: docs, Old: old, Node: in.Node, Facts: in.Facts(docs, old)}); err != nil {
		return res, err
	}
	if err := img.Validate(ctx, in.Node, docs); err != nil {
		return res, err
	}
	staged, err := img.Render(ctx, in.Node, docs, true)
	if err != nil {
		return res, err
	}
	o, warnings, err := Due(img.Effects, Delivery{Node: in.Node, Documents: docs, Schemas: img.Schemas, Outputs: staged.States()}, in.Compare)
	if err != nil {
		staged.Discard()
		return res, err
	}
	plan, err := PlanOf(img.Effects, o)
	if err != nil {
		staged.Discard()
		return res, err
	}
	res.Staged, res.Plan, res.Warnings = staged, plan, warnings
	return res, nil
}
