package chalkd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/rules"
)

// factsFor is the loader of the facts of one delivery to the running image, or of an install, by
// the facts the running image declares. Commands read next and old as rules see them, and the
// fallback secret when they declare it. An image that does not run gets the facts it declares
// itself, from stagedFacts.
func (s *Server) factsFor(ctx context.Context, next, old config.Redacted, secret string) *facts.Loader {
	run := s.Facts
	if run == nil {
		run = facts.Sandbox{}
	}
	l := facts.NewLoader(ctx, s.Loader.Contract.Facts, run)
	l.Input = factInput(next, old)
	l.Inputs = map[string]any{facts.FallbackSecret: secret}
	return l
}

// factInput is what command facts read of a check of next against old: both as rules see them.
func factInput(next, old config.Redacted) map[string]any {
	return map[string]any{"config": rules.ConfigOf(next), "oldConfig": rules.ConfigOf(old)}
}

// runningRules are the change rules of the running image's schemas, which its build checked
// already. Rules that do not compile are the node's own failure.
func (s *Server) runningRules() (*rules.Set, error) {
	set, err := s.Loader.Contract.Rules()
	if err != nil {
		return nil, fmt.Errorf("the running image's rules: %w", err)
	}
	return set, nil
}

// jsonValue is v as a fact read from JSON holds it, for a fact that stands in for a file, such as
// the storage a change is yet to make.
func jsonValue(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil
	}
	return out
}
