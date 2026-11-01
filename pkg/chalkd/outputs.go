package chalkd

import (
	"context"
	"log"

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
// The status records the failures either way, unless dry, as for a dry run, which changes
// nothing. What renderers and checks write to standard error goes to the journal alone. The
// renderers read docs with their secret values hashed, as they would read them in the image's
// checker.
func (s *Server) renderOutputs(ctx context.Context, name string, docs []config.Document, schemas *schema.Schemas, refuse, dry bool) (*outputs.Staged, error) {
	staged, failures, err := s.Loader.RenderOutputs(ctx, name, config.Redact(docs), schemas, true)
	if err != nil {
		return nil, failed(connect.CodeInternal, "the running image's outputs: %v", err)
	}
	if len(failures) == 0 {
		return staged, nil
	}
	if !dry {
		if err := outputs.RecordFailures(s.Loader.Render.Root, failures); err != nil {
			log.Printf("record the outputs' failures: %v", err)
		}
	}
	if !refuse {
		if dry {
			log.Printf("%s; a dry run of a delivery that keeps the node's documents plans it all the same", outputs.Refusal(failures))
		} else {
			log.Printf("%s; the delivery keeps the node's documents, so it goes ahead", outputs.Refusal(failures))
		}
		return staged, nil
	}
	staged.Discard()
	return nil, failed(connect.CodeFailedPrecondition, "%s", outputs.Refusal(failures))
}
