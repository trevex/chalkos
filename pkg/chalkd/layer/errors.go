package layer

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"
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

// coded is an error that carries its code: a *connect.Error, an error of Errorf, or a domain error
// of a layer with a Code method.
type coded interface {
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
// neither is a failure of the node itself, CodeInternal.
func (c Codes) Of(err error) connect.Code {
	if code, ok := c.find(err); ok {
		return code
	}
	return connect.CodeInternal
}

func (c Codes) find(err error) (connect.Code, bool) {
	if err == nil {
		return 0, false
	}
	if e, ok := err.(coded); ok {
		return e.Code(), true
	}
	for _, f := range c {
		if code, ok := f(err); ok {
			return code, true
		}
	}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		return c.find(u.Unwrap())
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			if code, ok := c.find(e); ok {
				return code, true
			}
		}
	}
	return 0, false
}

// Error is err as connect sends it: a *connect.Error as it is, and any other error with the code
// Of finds, its whole text and the details of a *connect.Error it wraps.
func (c Codes) Error(err error) *connect.Error {
	if ce, ok := err.(*connect.Error); ok {
		return ce
	}
	ce := connect.NewError(c.Of(err), err)
	if inner, ok := errors.AsType[*connect.Error](err); ok {
		for _, d := range inner.Details() {
			ce.AddDetail(d)
		}
	}
	return ce
}

// CodeOf is the code err answers an RPC with, as Codes.Of finds it without domain errors.
func CodeOf(err error) connect.Code { return Codes(nil).Of(err) }
