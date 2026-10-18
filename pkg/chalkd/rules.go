package chalkd

import (
	"bytes"
	"context"
	"encoding/json"

	"connectrpc.com/connect"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

// factsFor is the loader of one delivery's facts, by the facts the running image declares: they
// read this node, whichever image the config is for. Commands read next and old as rules see them,
// and the fallback secret when they declare it.
func (s *Server) factsFor(ctx context.Context, next, old []config.Document, secret string) (*facts.Loader, error) {
	defs, err := facts.Read(s.Paths.Facts)
	if err != nil {
		return nil, failed(connect.CodeInternal, "read the image's facts: %v", err)
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

// compileRules compiles the rules of an image's schemas, which its build checked already. Rules of
// the running image that do not compile are the node's own failure, Internal; another image's,
// which an install or upgrade brings, are the request's, InvalidArgument. Either compiles with
// the running chalkd's CEL library.
func compileRules(schemas *schema.Schemas, running bool) (*rules.Set, error) {
	set, err := rules.Compile(schemas)
	switch {
	case err != nil && running:
		return nil, failed(connect.CodeInternal, "the running image's rules: %v", err)
	case err != nil:
		return nil, failed(connect.CodeInvalidArgument, "the image's rules: %v", err)
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
		return failed(connect.CodeFailedPrecondition, "%s", rules.Refusal(failures, name))
	}
	return nil
}

// evaluate returns the rules of set next does not meet, which see the node called name; an error
// is a rule that could not be checked, which refuses the delivery too.
func evaluate(set *rules.Set, name string, next, old []config.Document, f rules.Facts) ([]rules.Failure, error) {
	failures, err := set.Evaluate(rules.Input{Documents: next, Old: old, Node: name, Facts: f})
	if err != nil {
		return nil, failed(connect.CodeFailedPrecondition, "%v", err)
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
