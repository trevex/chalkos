package rules

import (
	"fmt"
	"slices"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"

	"github.com/trevex/chalkos/pkg/config"
)

// An Effect is the rule of an effect, as the image's effects.json declares it: a CEL expression
// that is true when a delivery takes the effect, for a trigger that no output renders.
type Effect struct {
	// Name is the effect's.
	Name, When string
	// Facts are the names of the facts the rule reads, as facts.<name> after the delivery or
	// oldFacts.<name> before it, sorted.
	Facts []string

	program cel.Program
}

// Where names the effect the rule decides.
func (e *Effect) Where() string {
	return "effect " + e.Name
}

// CompileEffect compiles when, the rule of the effect name. It sees config, oldConfig and node
// as change rules do, and each fact it names as facts.<name> after the delivery and as
// oldFacts.<name> before it.
func CompileEffect(name, when string) (*Effect, error) {
	e := &Effect{Name: name, When: when}
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%s: rule %q: %s", e.Where(), when, fmt.Sprintf(format, args...))
	}
	if strings.TrimSpace(when) == "" {
		return nil, fail("the rule is empty")
	}
	base, err := newEnv("config", "oldConfig", "node")
	if err != nil {
		return nil, err
	}
	parsed, iss := base.Parse(when)
	if iss.Err() != nil {
		return nil, fail("%v", iss.Err())
	}
	if e.Facts, err = factNames(parsed, "facts", "oldFacts"); err != nil {
		return nil, fail("%v", err)
	}
	slices.Sort(e.Facts)
	opts := make([]cel.EnvOption, 0, 2*len(e.Facts))
	for _, n := range e.Facts {
		opts = append(opts, cel.Variable("facts."+n, cel.DynType), cel.Variable("oldFacts."+n, cel.DynType))
	}
	env, err := base.Extend(opts...)
	if err != nil {
		return nil, err
	}
	if e.program, err = program(env, parsed, cel.BoolType); err != nil {
		return nil, fail("%v", err)
	}
	return e, nil
}

// EffectInput is what effects are evaluated against once a delivery is applied: the config the
// node runs and the config it ran before, each with its schema defaults; the node's name; and the
// node's facts after the delivery and before it. OldHashed tells that the secret values of Old
// are hashed already, as config.Redact hashes them.
type EffectInput struct {
	Documents, Old []config.Document
	OldHashed      bool
	// Node is the node's name, which effects read as node.name, as Input.Node is.
	Node            string
	Facts, OldFacts Facts
}

// Due evaluates every effect against in and returns those that are due, in order. An effect
// whose rule cannot be evaluated, or reads a fact that cannot be loaded, is due too, so a unit
// restarts once more than it needed to rather than run on config it has not read; errs say why,
// one for each such effect.
func Due(effects []*Effect, in EffectInput) (due []*Effect, errs []error) {
	cfg, oldCfg := ConfigOf(in.Documents), configMap(objectsOf(in.Old, !in.OldHashed))
	node := nodeOf(in.Node)
	for _, e := range effects {
		ok, err := e.evaluate(cfg, oldCfg, node, in.Facts, in.OldFacts)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Where(), err))
			ok = true
		}
		if ok {
			due = append(due, e)
		}
	}
	return due, errs
}

func (e *Effect) evaluate(cfg, oldCfg, node map[string]any, facts, oldFacts Facts) (bool, error) {
	vars := map[string]any{"config": cfg, "oldConfig": oldCfg, "node": node}
	var factErr error
	bindFacts(vars, "facts", e.Facts, facts, &factErr)
	bindFacts(vars, "oldFacts", e.Facts, oldFacts, &factErr)
	out, _, err := e.program.Eval(vars)
	if factErr != nil {
		return false, factErr
	}
	if err != nil {
		return false, fmt.Errorf("%s", cannotEvaluate(err))
	}
	ok, isBool := out.(types.Bool)
	if !isBool {
		return false, fmt.Errorf("a rule yields %s, not a bool", out.Type().TypeName())
	}
	return bool(ok), nil
}
