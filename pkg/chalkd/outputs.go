package chalkd

import (
	"context"
	"errors"
	"log"

	"github.com/trevex/chalkos/pkg/check"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/outputs"
)

// renderOutputs renders img's outputs from docs for the node called name, checks them and stages
// them, as img.Render does. A failure is handled by refuse:
//   - set, it refuses the delivery whole and the node keeps serving its outputs;
//   - unset, as for a delivery that keeps the node's documents, the outputs that passed are staged
//     and the failures logged.
//
// The status records the failures either way, unless dry, as for a dry run, which changes
// nothing. What renderers and checks write to standard error goes to the journal alone. The
// renderers read docs redacted, as they would read them in the image's checker.
func (s *Server) renderOutputs(ctx context.Context, img check.Image, name string, docs config.Redacted, refuse, dry bool) (*outputs.Staged, error) {
	staged, err := img.Render(ctx, name, docs, refuse)
	r, ok := errors.AsType[*check.Refusal](err)
	if !ok {
		return staged, err
	}
	if !dry {
		s.recordRefused(err)
	}
	switch {
	case refuse:
		return nil, err
	case dry:
		log.Printf("%s; a dry run of a delivery that keeps the node's documents plans it all the same", r.Message)
	default:
		log.Printf("%s; the delivery keeps the node's documents, so it goes ahead", r.Message)
	}
	return staged, nil
}
