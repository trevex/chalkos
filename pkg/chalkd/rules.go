package chalkd

import (
	"context"
	"slices"
	"strings"

	"connectrpc.com/connect"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/facts"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

// factsFor is the loader of one delivery's facts, by the facts the running image declares: they
// read this node, whichever image the config is for. Commands read next and old as rules see them,
// and the fallback secret when they declare it. On the installer, which has no STATE, every file
// fact is null.
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
	l.Input = map[string]any{"config": rules.ConfigOf(next), "oldConfig": rules.ConfigOf(old)}
	l.Inputs = map[string]any{facts.FallbackSecret: secret}
	l.NoFiles = s.Installer
	return l, nil
}

// checkRules refuses next, config about to be stored for the image whose schemas are given, when
// it does not meet that image's rules; old is the running slot's config, nil at an install, and f
// the delivery's facts. Nothing has changed yet when it refuses.
func (s *Server) checkRules(schemas *schema.Schemas, next, old []config.Document, f rules.Facts) error {
	failures, err := evaluateRules(schemas, next, old, f)
	if err != nil {
		return err
	}
	if len(failures) > 0 {
		return failed(connect.CodeFailedPrecondition, "%s", refusal(failures, s.nodeName()))
	}
	return nil
}

// evaluateRules compiles the rules of schemas and returns those next does not meet.
func evaluateRules(schemas *schema.Schemas, next, old []config.Document, f rules.Facts) ([]rules.Failure, error) {
	set, err := rules.Compile(schemas)
	if err != nil {
		return nil, failed(connect.CodeInvalidArgument, "the image's rules: %v", err)
	}
	failures, err := set.Evaluate(rules.Input{Documents: next, Old: old, Facts: f})
	if err != nil {
		return nil, failed(connect.CodeFailedPrecondition, "%v", err)
	}
	return failures, nil
}

// refusal says what the rules a config fails take, the most a change takes first: a refusal,
// whose message says what to do instead, a reinstall of the node, or chalkctl storage reset.
// Each rule's message names the change and its remedy.
func refusal(failures []rules.Failure, node string) string {
	takes := func(t rules.Takes) bool {
		return slices.ContainsFunc(failures, func(f rules.Failure) bool { return f.Takes == t })
	}
	head := "the config takes chalkctl storage reset"
	switch {
	case takes(rules.Refuse):
		head = "the config is refused"
	case takes(rules.Reinstall):
		head = "the config takes a reinstall of " + node
	}
	msgs := make([]string, len(failures))
	for i, f := range failures {
		msgs[i] = f.String()
	}
	return head + ": " + strings.Join(msgs, "; ")
}
