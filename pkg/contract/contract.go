// Package contract loads an image's contract, what the image declares about config and itself,
// from one directory (/etc/chalkos on a node):
//
//   - schemas/, the schemas of its kinds and the change rules they declare;
//   - facts.json, the facts those rules and the effects' rules read;
//   - effects.json, what a delivery does to services, and services.json, the units granted kinds;
//   - outputs.json and validators.json, the programs that read the node's config documents;
//   - diagnostics.json, the commands an admin may run on the node.
//
// A missing file other than schemas/ declares none. The change rules and the effects' rules compile
// when first asked for, as a boot renders the outputs without them.
//
// The image's modules refuse a definition when the image is evaluated, with a message that names
// the option. Load checks again, as a defence, what chalkd needs to carry the definitions out
// safely, such as names that form paths, the outputs' dirs, programs by absolute path, and that
// no program or granted unit reads Secret or withheld documents. chalkos-rules check adds what needs
// the image's store and units.
package contract

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sync"

	"github.com/trevex/chalkos/pkg/diagnostics"
	"github.com/trevex/chalkos/pkg/effects"
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

// DefaultDir is the directory of the running image's contract on a node.
const DefaultDir = "/etc/chalkos"

// The files of a contract's directory.
const (
	SchemasDir      = "schemas"
	FactsFile       = "facts.json"
	EffectsFile     = "effects.json"
	ServicesFile    = "services.json"
	OutputsFile     = "outputs.json"
	ValidatorsFile  = "validators.json"
	DiagnosticsFile = "diagnostics.json"
)

// Contract is an image's contract, loaded and checked. It is safe for concurrent use.
type Contract struct {
	// Dir is the directory it was loaded from; empty for one of schemas alone.
	Dir         string
	Schemas     *schema.Schemas
	Facts       map[string]facts.Definition
	Effects     map[string]effects.Definition
	Services    map[string]effects.Service
	Outputs     map[string]outputs.Definition
	Validators  map[string]outputs.Validator
	Diagnostics map[string]diagnostics.Definition

	// The rules are compiled when first asked for, so a Contract built as a literal compiles them
	// as one Load returns does.
	rulesOnce   sync.Once
	ruleSet     *rules.Set
	rulesErr    error
	effectsOnce sync.Once
	effectRules []*rules.EffectRule
	effectsErr  error
}

// Rules are the change rules of the schemas, compiled once, when first asked for; the error is
// every rule that does not compile. A contract without schemas has none.
func (c *Contract) Rules() (*rules.Set, error) {
	c.rulesOnce.Do(func() { c.ruleSet, c.rulesErr = rules.Compile(c.Schemas) })
	return c.ruleSet, c.rulesErr
}

// EffectRules are the rules of the effects that have one, in the order of the effects' names,
// compiled once, when first asked for; the error is every rule that does not compile.
func (c *Contract) EffectRules() ([]*rules.EffectRule, error) {
	c.effectsOnce.Do(func() {
		var errs []error
		for _, name := range slices.Sorted(maps.Keys(c.Effects)) {
			// An effect without a rule fires on its outputs or kinds alone.
			if when := c.Effects[name].When; when != "" {
				r, err := rules.CompileEffect(name, when)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				c.effectRules = append(c.effectRules, r)
			}
		}
		if c.effectsErr = errors.Join(errs...); c.effectsErr != nil {
			c.effectRules = nil
		}
	})
	return c.effectRules, c.effectsErr
}

// An Option changes how Load reads a contract.
type Option func(*options)

type options struct {
	renderRoot string
}

// RenderRoot is the directory below which the node renders the outputs, which Load checks their
// dirs against; outputs.DefaultRoot without it.
func RenderRoot(root string) Option {
	return func(o *options) { o.renderRoot = root }
}

// Load loads the contract of the directory dir. It refuses schemas that do not compile and every
// definition chalkd could not carry out, reporting all it refuses.
func Load(dir string, opts ...Option) (*Contract, error) {
	o := options{renderRoot: outputs.DefaultRoot}
	for _, opt := range opts {
		opt(&o)
	}
	c := &Contract{Dir: dir}
	var err error
	if c.Schemas, err = schema.LoadSchemas(filepath.Join(dir, SchemasDir)); err != nil {
		err = fmt.Errorf("the image's schemas: %w", err)
	}
	// errors.Join leaves out the nil errors.
	errs := []error{err}
	c.Facts, err = facts.Read(filepath.Join(dir, FactsFile))
	errs = append(errs, err)
	c.Effects, err = effects.Read(filepath.Join(dir, EffectsFile))
	errs = append(errs, err)
	c.Services, err = effects.ReadServices(filepath.Join(dir, ServicesFile))
	errs = append(errs, err)
	c.Outputs, err = outputs.Read(filepath.Join(dir, OutputsFile), o.renderRoot)
	errs = append(errs, err)
	c.Validators, err = outputs.ReadValidators(filepath.Join(dir, ValidatorsFile))
	errs = append(errs, err)
	c.Diagnostics, err = diagnostics.Read(filepath.Join(dir, DiagnosticsFile))
	errs = append(errs, err)
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	if err := c.checkWithheld(); err != nil {
		return nil, err
	}
	return c, nil
}

// checkWithheld refuses an output or a validator that reads the documents of a kind the schemas
// declare withheld, and a unit granted one, since the node keeps those documents, and the values
// their secretRefs name, for chalkd alone.
func (c *Contract) checkWithheld() error {
	withheld := map[string]bool{}
	for _, k := range c.Schemas.Kinds() {
		// Inputs name kinds without their version: a name is withheld when one of its versions is.
		withheld[k.Kind] = withheld[k.Kind] || k.Withheld
	}
	var errs []error
	check := func(file, what, name string, kinds []string) {
		for _, kind := range kinds {
			if withheld[kind] {
				errs = append(errs, fmt.Errorf("%s: %s %s: the image withholds %s documents from its units and programs", filepath.Join(c.Dir, file), what, name, kind))
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(c.Outputs)) {
		check(OutputsFile, string(outputs.RoleOutput), name, c.Outputs[name].Inputs)
	}
	for _, name := range slices.Sorted(maps.Keys(c.Validators)) {
		check(ValidatorsFile, string(outputs.RoleValidator), name, c.Validators[name].Inputs)
	}
	for _, name := range slices.Sorted(maps.Keys(c.Services)) {
		check(ServicesFile, "service", name, c.Services[name].Kinds)
	}
	return errors.Join(errs...)
}

// FromSchemas is the contract of an image of which only the schema files are at hand, by name, as
// a node's Contract call and an image's artifact carry them: its schemas and their rules, and no
// other definition.
func FromSchemas(files map[string][]byte) (*Contract, error) {
	s, err := schema.LoadFiles(files)
	if err != nil {
		return nil, err
	}
	c := &Contract{Schemas: s}
	return c, nil
}
