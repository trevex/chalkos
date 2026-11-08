package chalkd

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/validators"
)

// checkValidators refuses docs, config about to be stored for the running image, when one of the
// image's validators refuses it or fails, as check.Run does in the image's checker: after the
// change rules, before the renderers, and before anything changes. The validators read docs
// redacted; what their programs write to standard error goes to the journal
// alone.
func (s *Server) checkValidators(ctx context.Context, docs config.Redacted) error {
	rs, failures, err := s.Loader.Validate(ctx, docs)
	if err != nil {
		return fmt.Errorf("the running image's validators: %w", err)
	}
	if len(rs) > 0 || len(failures) > 0 {
		return layer.Errorf(connect.CodeFailedPrecondition, "%s", validators.Refusal(rs, failures))
	}
	return nil
}
