package layer

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type domainError struct{ error }

func TestCodes(t *testing.T) {
	codes := Codes{CodeFor[domainError](connect.CodeFailedPrecondition)}
	refused := Errorf(connect.CodeInvalidArgument, "the request names no image")
	for name, tc := range map[string]struct {
		err  error
		want connect.Code
	}{
		"no error":             {nil, 0},
		"no code":              {fs.ErrNotExist, connect.CodeInternal},
		"a code":               {refused, connect.CodeInvalidArgument},
		"a wrapped code":       {fmt.Errorf("upgrade: %w", refused), connect.CodeInvalidArgument},
		"the outermost code":   {Errorf(connect.CodeInternal, "the image's facts: %w", refused), connect.CodeInternal},
		"a connect error":      {fmt.Errorf("ask: %w", connect.NewError(connect.CodeUnavailable, fs.ErrNotExist)), connect.CodeUnavailable},
		"a domain error":       {fmt.Errorf("activate: %w", domainError{fs.ErrNotExist}), connect.CodeFailedPrecondition},
		"a code in a join":     {errors.Join(fs.ErrNotExist, refused), connect.CodeInvalidArgument},
		"a domain error first": {fmt.Errorf("%w: %w", domainError{fs.ErrNotExist}, refused), connect.CodeFailedPrecondition},
	} {
		if got := codes.Of(tc.err); got != tc.want {
			t.Errorf("%s: Of(%v) = %v, want %v", name, tc.err, got, tc.want)
		}
	}
}

// Errorf keeps the error it wraps, and its text is that of fmt.Errorf.
func TestErrorfWraps(t *testing.T) {
	err := Errorf(connect.CodeFailedPrecondition, "read the slot: %w", fs.ErrNotExist)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%v does not wrap fs.ErrNotExist", err)
	}
	if got, want := err.Error(), "read the slot: "+fs.ErrNotExist.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// Error answers with the whole text and the code, keeps a connect error as it is and carries the
// details of one it wraps.
func TestCodesError(t *testing.T) {
	ce := Codes(nil).Error(fmt.Errorf("upgrade: %w", Errorf(connect.CodeInvalidArgument, "no image")))
	if ce.Code() != connect.CodeInvalidArgument || ce.Message() != "upgrade: no image" {
		t.Errorf("Error() = %v", ce)
	}
	direct := connect.NewError(connect.CodeUnimplemented, errors.New("not served"))
	if got := Codes(nil).Error(direct); got != direct {
		t.Errorf("Error(%v) = %v, want it as it is", direct, got)
	}
	detailed := connect.NewError(connect.CodeFailedPrecondition, errors.New("not a member"))
	detail, err := connect.NewErrorDetail(wrapperspb.String("ask another"))
	if err != nil {
		t.Fatal(err)
	}
	detailed.AddDetail(detail)
	if got := Codes(nil).Error(fmt.Errorf("remove: %w", detailed)); len(got.Details()) != 1 || got.Code() != connect.CodeFailedPrecondition {
		t.Errorf("Error() = %v with %d details, want the wrapped error's code and detail", got, len(got.Details()))
	}
}
