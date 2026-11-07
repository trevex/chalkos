package chalkd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"

	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/check"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

// factsFor is the loader of the facts of one delivery to the running image, or of an install, by
// the facts the running image declares. Commands read next and old as rules see them, and the
// fallback secret when they declare it. An image that does not run gets the facts it declares
// itself, from stagedFacts.
func (s *Server) factsFor(ctx context.Context, next, old []config.Document, secret string) (*facts.Loader, error) {
	defs, err := facts.Read(s.Paths.Facts)
	if err != nil {
		return nil, fmt.Errorf("read the image's facts: %w", err)
	}
	run := s.Facts
	if run == nil {
		run = facts.Sandbox{}
	}
	l := facts.NewLoader(ctx, defs, run)
	l.Input = factInput(next, old)
	l.Inputs = map[string]any{facts.FallbackSecret: secret}
	return l, nil
}

// factInput is what command facts read of a check of next against old: both as rules see them.
func factInput(next, old []config.Document) map[string]any {
	return map[string]any{"config": rules.ConfigOf(next), "oldConfig": rules.ConfigOf(old)}
}

// compileRunningRules compiles the rules of the running image's schemas, which its build checked
// already. Rules that do not compile are the node's own failure.
func compileRunningRules(schemas *schema.Schemas) (*rules.Set, error) {
	set, err := rules.Compile(schemas)
	if err != nil {
		return nil, fmt.Errorf("the running image's rules: %w", err)
	}
	return set, nil
}

// checkRules refuses next, config about to be stored for the image whose rules set holds, when
// it does not meet them; name is the node's, old is the running slot's config, nil at an install,
// and f the delivery's facts. Nothing has changed yet when it refuses.
func (s *Server) checkRules(set *rules.Set, name string, next, old []config.Document, f rules.Facts) error {
	failures, err := evaluate(set, name, next, old, f)
	if err != nil {
		return err
	}
	if len(failures) > 0 {
		return layer.Errorf(connect.CodeFailedPrecondition, "%s", rules.Refusal(failures, name))
	}
	return nil
}

// evaluate returns the rules of set next does not meet, which see the node called name; an error
// is a rule that could not be checked, which refuses the delivery too.
func evaluate(set *rules.Set, name string, next, old []config.Document, f rules.Facts) ([]rules.Failure, error) {
	failures, err := check.Rules(set, name, config.Redact(next), config.Redact(old), f)
	if err != nil {
		return nil, layer.Errorf(connect.CodeFailedPrecondition, "%w", err)
	}
	return failures, nil
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
