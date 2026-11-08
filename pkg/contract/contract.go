// Package contract loads an image's contract: what the image declares about config and about
// itself, from the files of one directory, /etc/chalkos on a node, etc/chalkos of an image's
// NixOS system, or the directory chalkos-rules checks as an image's build lays it out:
//
//   - schemas/, the schemas of its kinds, and the change rules they declare;
//   - facts.json, the facts those rules and the effects' rules read;
//   - effects.json, what a delivery does to the node's services, and services.json, the units
//     granted config kinds;
//   - outputs.json and validators.json, the programs that read the node's config documents;
//   - diagnostics.json, the commands an admin may run on the node.
//
// Load checks every definition as chalkd needs it; a file other than the schemas that is missing
// declares none. It compiles the change rules and the effects' rules when they are first asked
// for, as a boot renders the outputs without them. An image's contract does not change while it
// runs, so chalkd loads the running image's once.
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

	rules       func() (*rules.Set, error)
	effectRules func() ([]*rules.EffectRule, error)
}

// Rules are the change rules of the schemas, compiled once, when first asked for; the error is
// every rule that does not compile.
func (c *Contract) Rules() (*rules.Set, error) { return c.rules() }

// EffectRules are the rules of the effects that have one, in the order of the effects' names,
// compiled once, when first asked for; the error is every rule that does not compile.
func (c *Contract) EffectRules() ([]*rules.EffectRule, error) { return c.effectRules() }

// compileLater has c compile its rules when they are first asked for.
func (c *Contract) compileLater() {
	c.rules = sync.OnceValues(func() (*rules.Set, error) { return rules.Compile(c.Schemas) })
	c.effectRules = sync.OnceValues(func() ([]*rules.EffectRule, error) {
		var (
			compiled []*rules.EffectRule
			errs     []error
		)
		for _, name := range slices.Sorted(maps.Keys(c.Effects)) {
			// An effect without a rule fires on its outputs or kinds alone.
			if when := c.Effects[name].When; when != "" {
				r, err := rules.CompileEffect(name, when)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				compiled = append(compiled, r)
			}
		}
		if len(errs) > 0 {
			return nil, errors.Join(errs...)
		}
		return compiled, nil
	})
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
	c.compileLater()
	return c, nil
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
	c.compileLater()
	return c, nil
}
