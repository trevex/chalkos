package chalkd

import (
	"context"
	"log"
	"strings"

	"connectrpc.com/connect"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/schema"
)

// renderOutputs renders the running image's outputs from docs for the node called name, checks
// them and stages them. schemas tell the secretRef fields of docs; nil means the running image's.
// A failure is handled by refuse:
//   - set, it refuses the delivery whole and the node keeps serving its outputs;
//   - unset, as for a delivery that keeps the node's documents, the outputs that passed are staged
//     and the failures logged.
//
// The status records the failures either way. What renderers and checks write to standard error
// goes to the journal alone.
func (s *Server) renderOutputs(ctx context.Context, name string, docs []config.Document, schemas *schema.Schemas, refuse bool) (*outputs.Staged, error) {
	staged, failures, err := s.Loader.RenderOutputs(ctx, name, docs, schemas)
	if err != nil {
		return nil, failed(connect.CodeInternal, "the running image's outputs: %v", err)
	}
	if len(failures) == 0 {
		return staged, nil
	}
	if err := outputs.RecordFailures(s.Loader.Render.Root, failures); err != nil {
		log.Printf("record the outputs' failures: %v", err)
	}
	msgs := make([]string, len(failures))
	for i, f := range failures {
		msgs[i] = f.Error()
	}
	if !refuse {
		log.Printf("%s; the delivery keeps the node's documents, so it goes ahead", strings.Join(msgs, "; "))
		return staged, nil
	}
	staged.Discard()
	return nil, failed(connect.CodeFailedPrecondition, "%s", strings.Join(msgs, "; "))
}
