package sdk_test

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectproto"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/check"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/refusals"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/sdk"
)

// nodeError is an error as a node answers with it, with the reason when there is one.
func nodeError(t *testing.T, code connect.Code, reason nodev1.ErrorReason) error {
	t.Helper()
	err := connect.NewError(code, "refused")
	if reason == nodev1.ErrorReason_ERROR_REASON_UNSPECIFIED {
		return err
	}
	detail, derr := connectproto.NewErrorDetail(&nodev1.ReasonDetail{Reason: reason})
	if derr != nil {
		t.Fatal(derr)
	}
	return err.WithDetail(detail)
}

// A refusal of config by an image that stands is told apart from other failures, locally and on a
// node, by its reason, never by its code or message; a refusal that may pass next time is not one.
func TestRefused(t *testing.T) {
	if client.ReasonNamed(check.RefusedReason) != nodev1.ErrorReason_ERROR_REASON_CONFIG_REFUSED {
		t.Errorf("check.RefusedReason %s names no reason of the node API", check.RefusedReason)
	}
	unmet := &check.Refusal{Stage: check.StageRules, Message: "the rule fails", Failures: []rules.Failure{{Refusal: refusals.Refusal{Message: "the rule fails"}}}}
	vetoed := &check.Refusal{Stage: check.StageValidators, Failed: []outputs.Failure{{Name: "veto", Refusals: []refusals.Refusal{{Kind: "Demo", Name: "one"}}}}}
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"rules not met":                     {fmt.Errorf("the config of w1: %w", unmet), true},
		"a set refused":                     {&check.Refusal{Stage: check.StageConfig, Message: "no such kind"}, true},
		"a validator's refusal":             {vetoed, true},
		"a rule's reason":                   {&check.Refusal{Stage: check.StageRules, Reason: "NO_FALLBACK_SECRET"}, true},
		"a rule that cannot be evaluated":   {&check.Refusal{Stage: check.StageRules, Message: "cannot check: fact share"}, false},
		"a program that failed":             {&check.Refusal{Stage: check.StageOutputs, Failed: []outputs.Failure{{Name: "demo", Problem: "its renderer failed (exit 3)"}}}, false},
		"a node's refusal":                  {nodeError(t, connect.CodeFailedPrecondition, nodev1.ErrorReason_ERROR_REASON_CONFIG_REFUSED), true},
		"a node's refusal by a set":         {nodeError(t, connect.CodeInvalidArgument, nodev1.ErrorReason_ERROR_REASON_CONFIG_REFUSED), true},
		"a node's refusal by a rule reason": {nodeError(t, connect.CodeFailedPrecondition, nodev1.ErrorReason_ERROR_REASON_NO_FALLBACK_SECRET), true},
		"another reason":                    {nodeError(t, connect.CodeFailedPrecondition, nodev1.ErrorReason_ERROR_REASON_TRIAL_RECORD_UNREADABLE), false},
		"a precondition alone":              {nodeError(t, connect.CodeFailedPrecondition, nodev1.ErrorReason_ERROR_REASON_UNSPECIFIED), false},
		"another error":                     {errors.New("connection refused"), false},
		"no error":                          {nil, false},
	} {
		if got := sdk.Refused(tc.err); got != tc.want {
			t.Errorf("%s: Refused(%v) = %v, want %v", name, tc.err, got, tc.want)
		}
	}
}
