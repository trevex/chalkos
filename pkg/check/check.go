// Package check checks a node's config as the image the config is for checks it, from what that
// image holds and what the node gives it, and changes nothing. A check has four stages, each a
// method of Image, in this order:
//
//  1. Translate: the documents against the schemas, the set's own rules, and the identity they
//     translate to;
//  2. Rules: the image's change rules, against the config the new one replaces;
//  3. Validate: the image's validators;
//  4. Render: the image's renderers and their checks.
//
// Each stage returns a *Refusal for a config it refuses and any other error for a failure of the
// image or of the check itself. Run runs them in a row, for an image that checks its own config,
// and plans the effects a delivery of the config takes; RunTranslated runs the last three and plans
// the effects, for documents checked as a set without a node's identity, as an image's tests check
// theirs with Image.CheckRedacted. chalkd runs the stages for a delivery to the running image,
// with its own checks of the machine between them; chalkctl runs the first three, without the
// node's facts and with the validators it links.
//
// A check never sees a Secret's value: it takes the documents as config.Redacted after their
// translation, which is all rules, validators and renderers read of them, and the checks of secret
// references need the Secrets' keys alone.
package check

import (
	"context"
	"errors"
	"fmt"

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

// Validate runs the programs of p as outputs.Renderer.Validate does.
func (p Programs) Validate(ctx context.Context, in outputs.Input) ([]outputs.Failure, error) {
	return p.Render.Validate(ctx, p.Defs, in)
}

// Linked are validators linked into the program that checks, which run in-process as
// outputs.RunLinked runs them.
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

// Refusal is a config a check refused: the stage that refused it, and why, as that stage says it.
// A refused translation says what is wrong with the set, which the caller puts in its own words;
// the other stages' messages say that the config is refused, or what failed. Failures are the
// change rules the config does not meet, for StageRules; Failed the validators or outputs that
// failed, for StageValidators and StageOutputs.
type Refusal struct {
	Stage    Stage
	Message  string
	Failures []rules.Failure
	Failed   []outputs.Failure
}

func (r *Refusal) Error() string { return r.Message }

// Rules checks in.Documents, a config set with its defaults, against the image's change rules,
// which compare it with in.Old and read in.Facts; without facts, the rules that read them do not
// run. A rule the config does not meet refuses it, and so does one that cannot be checked or a
// fact that cannot be loaded.
func (img Image) Rules(in rules.Input) error {
	if img.ChangeRules == nil {
		return nil
	}
	failures, err := img.ChangeRules.Evaluate(in)
	if err != nil {
		return &Refusal{Stage: StageRules, Message: err.Error()}
	}
	if len(failures) > 0 {
		return &Refusal{Stage: StageRules, Message: rules.Refusal(failures, in.Node), Failures: failures}
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

// Render renders every output of the image from docs, a config set with its defaults, for the
// node called node, checks it and stages it; it changes nothing a service reads. An output that
// fails refuses the config. With refuse, a refusal stages nothing: Render discards what it staged
// and returns no outputs. Without it, as for a delivery that keeps the node's documents, Render
// returns the outputs that passed, staged, along with the refusal of those that did not, which the
// caller reports and goes ahead.
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

// Input is a config to check for the node called Node:
//   - Delivered are the documents as delivered, and Old the documents of the config they replace,
//     as delivered too, or with the defaults of the image that checks; empty at an install.
//   - Facts gives the node's facts for a check of the translated documents against Old, with its
//     defaults, which command facts read; it loads a fact when a rule first reads it. Run requires
//     it.
//   - Compare are what the effects compare the config with: the config the node runs, or a
//     baseline and what the deliveries that failed since applied; none plans no effects.
type Input struct {
	Node      string
	Delivered config.Redacted
	Old       config.Redacted
	Facts     func(next, old config.Redacted) rules.Facts
	Compare   []Comparison
}

// Result is a config that passed: its documents with their defaults, redacted, the identity they
// translate to (JSON), its outputs, rendered and checked, and the plan of a delivery of it.
// Warnings name the effect rules that could not be evaluated, whose effects the plan takes all
// the same, as Due gives them. The caller discards or swaps in Staged.
type Result struct {
	Documents config.Redacted
	Identity  []byte
	Staged    *outputs.Staged
	Plan      Plan
	Warnings  []string
}

// errNoFacts is the error of a check whose input gives no facts.
var errNoFacts = errors.New("the check has no facts to give the rules")

// Run checks in as img checks it, its stages in their order: it translates in.Delivered, runs the
// image's change rules against in.Old with the image's defaults, its validators, and every
// output's renderer and check, and plans the effects. A refused config is a *Refusal; any other
// error is the image's own failure. A config refused after its translation still gives the
// translation, whose storage a caller names in the refusal.
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

// RunTranslated is Run after the translation: it runs the image's change rules on docs, the set
// translated, or checked by Image.CheckRedacted, against in.Old with the image's defaults, its
// validators, and every output's renderer and check, and plans the effects. It reads docs in place
// of in.Delivered, which it does not read, and the result holds no identity. A refused config is a
// *Refusal; any other error is the image's own failure.
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
	res.Staged, res.Plan, res.Warnings = staged, PlanOf(img.Effects, o), warnings
	return res, nil
}
