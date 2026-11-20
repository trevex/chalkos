package layer

import (
	"fmt"

	"connectrpc.com/connect/v2"
)

// Errorf is an error that answers an RPC with code: the handlers of chalkd and of its plugins
// return it for a decision of their own, such as an argument they refuse, and wrap everything
// else with fmt.Errorf. It formats as fmt.Errorf does, so %w keeps the error it wraps.
func Errorf(code connect.Code, format string, args ...any) error {
	return &codedError{code: code, err: fmt.Errorf(format, args...)}
}

// codedError is an error and the code it answers an RPC with.
type codedError struct {
	code connect.Code
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }

func (e *codedError) Unwrap() error { return e.err }

func (e *codedError) Code() connect.Code { return e.code }

// Coded is an error that carries its code: a *connect.Error, an error of Errorf, or a domain error
// of a layer with a Code method.
type Coded interface {
	error
	Code() connect.Code
}

// Codes are the codes of domain errors that carry none themselves, such as the errors of a package
// that knows nothing of RPCs.
type Codes []func(error) (connect.Code, bool)

// CodeFor is code for an error of type T.
func CodeFor[T error](code connect.Code) func(error) (connect.Code, bool) {
	return func(err error) (connect.Code, bool) {
		_, ok := err.(T)
		return code, ok
	}
}

// Of is the code of the first error in err's tree, depth first, that carries one or that c maps:
// the outermost, so a handler that wraps an error with a code of its own decides. An error with
// neither is a failure of the node itself, CodeInternal. No error has no code, 0.
func (c Codes) Of(err error) connect.Code {
	if err == nil {
		return 0
	}
	code := connect.CodeInternal
	firstIn(err, func(e error) bool {
		if ce, ok := e.(Coded); ok {
			code = ce.Code()
			return true
		}
		for _, f := range c {
			if cf, ok := f(e); ok {
				code = cf
				return true
			}
		}
		return false
	})
	return code
}

// firstIn is the first error in err's tree, depth first, that match matches, or nil.
func firstIn(err error, match func(error) bool) error {
	if err == nil || match(err) {
		return err
	}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		return firstIn(u.Unwrap(), match)
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			if found := firstIn(e, match); found != nil {
				return found
			}
		}
	}
	return nil
}

// Detailed is a coded error with details for the client, such as what to do next.
type Detailed interface {
	Coded
	Details() []*connect.ErrorDetail
}

// Error is err as connect sends it: a *connect.Error of the handler as it is, one of a peer, such
// as another node, with its code, message and details, and any other error with the code Of finds,
// its whole text and the details of the first error in its tree with that code. connect sends no
// other error's text, and answers with a peer's error as CodeInternal without its message.
func (c Codes) Error(err error) *connect.Error {
	if ce, ok := err.(*connect.Error); ok {
		if !ce.IsRemote() {
			return ce
		}
		return withDetails(connect.NewError(ce.Code(), ce.Message()).WithCause(ce), ce.Details())
	}
	code := c.Of(err)
	ce := connect.NewError(code, err.Error()).WithCause(err)
	if d, ok := firstIn(err, func(e error) bool {
		d, ok := e.(Detailed)
		return ok && d.Code() == code
	}).(Detailed); ok {
		ce = withDetails(ce, d.Details())
	}
	return ce
}

// withDetails is ce with details added.
func withDetails(ce *connect.Error, details []*connect.ErrorDetail) *connect.Error {
	for _, d := range details {
		ce = ce.WithDetail(d)
	}
	return ce
}

// CodeOf is the code err answers an RPC with, as Codes.Of finds it without domain errors: 0 for no
// error.
func CodeOf(err error) connect.Code { return Codes(nil).Of(err) }
