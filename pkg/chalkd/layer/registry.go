package layer

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/layers"
)

// Registry is the plugins compiled into chalkd and the active ones among them: those whose layers
// the image carries. A nil Registry has neither, as an image of the base alone.
type Registry struct {
	compiled, active []Plugin
	layers           layers.Set
}

// NewRegistry returns the registry of the compiled plugins on an image of the layers given. It
// refuses two plugins of one layer, a layer no compiled plugin serves, and two active plugins
// that both issue node certificates.
func NewRegistry(image layers.Set, compiled ...Plugin) (*Registry, error) {
	r := &Registry{layers: image}
	served := map[string]bool{}
	for _, p := range compiled {
		if served[p.Name()] {
			return nil, fmt.Errorf("two plugins serve the %s layer", p.Name())
		}
		served[p.Name()] = true
		r.compiled = append(r.compiled, p)
		if image.Has(p.Name()) {
			r.active = append(r.active, p)
		}
	}
	byName := func(a, b Plugin) int { return cmp.Compare(a.Name(), b.Name()) }
	slices.SortFunc(r.compiled, byName)
	slices.SortFunc(r.active, byName)
	for _, name := range image.Names() {
		if !served[name] {
			return nil, fmt.Errorf("the image carries the %s layer, which this chalkd has no plugin for", name)
		}
	}
	var issuers []string
	for _, p := range r.active {
		if _, ok := p.(Issuer); ok {
			issuers = append(issuers, p.Name())
		}
	}
	if len(issuers) > 1 {
		return nil, fmt.Errorf("the %s layers each issue node certificates; an image carries one issuer at most", strings.Join(issuers, " and "))
	}
	return r, nil
}

// Layers are the image's layers.
func (r *Registry) Layers() layers.Set {
	if r == nil {
		return layers.Set{}
	}
	return r.layers
}

// Compiled are the compiled plugins, in the order of their names.
func (r *Registry) Compiled() []Plugin {
	if r == nil {
		return nil
	}
	return slices.Clone(r.compiled)
}

// Active are the plugins of the image's layers, in the order of their names.
func (r *Registry) Active() []Plugin {
	if r == nil {
		return nil
	}
	return slices.Clone(r.active)
}

// Active returns the active plugins that implement the hook T, in the order of their names.
func Active[T any](r *Registry) []T { return hooks[T](r.Active()) }

// Compiled returns the compiled plugins that implement the hook T, in the order of their names.
func Compiled[T any](r *Registry) []T { return hooks[T](r.Compiled()) }

func hooks[T any](plugins []Plugin) []T {
	var list []T
	for _, p := range plugins {
		if h, ok := p.(T); ok {
			list = append(list, h)
		}
	}
	return list
}

// Issuer is the active plugin that issues node certificates; nil without one.
func (r *Registry) Issuer() Issuer {
	if list := Active[Issuer](r); len(list) > 0 {
		return list[0]
	}
	return nil
}

// Action returns the action an effect names as <layer>:<action>, of an active plugin.
func (r *Registry) Action(name string) (func(context.Context) error, error) {
	layer, action, ok := strings.Cut(name, ":")
	if !ok {
		return nil, fmt.Errorf("%q names no action as <layer>:<action>", name)
	}
	for _, p := range r.Active() {
		if a, ok := p.(Actions); ok && p.Name() == layer {
			if run := a.Actions()[action]; run != nil {
				return run, nil
			}
		}
	}
	return nil, fmt.Errorf("no layer of this image has the action %s", name)
}

// Command returns the subcommand chalkd <layer> <name> of a compiled plugin.
func (r *Registry) Command(layer, name string) (Command, bool) {
	for _, p := range r.Compiled() {
		if c, ok := p.(Commands); ok && p.Name() == layer {
			cmd, ok := c.Commands()[name]
			return cmd, ok
		}
	}
	return Command{}, false
}
