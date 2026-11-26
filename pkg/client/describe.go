package client

import (
	"errors"
	"net"
	"net/url"
	"strings"

	"connectrpc.com/connect/v2"

	"github.com/trevex/chalkos/pkg/pki"
)

// Hints say what Describe may add to an error of a call to a node.
type Hints struct {
	// Credentials names what the call authenticated with, such as "the client file ops.json", and
	// Role the role they grant, which a permission refusal names; empty for neither.
	Credentials string
	Role        pki.Role
	// Client is the program that called, such as chalkctl, and Release its release, which a call
	// the node does not implement names; empty for neither.
	Client, Release string
}

// Describe is err, the error of a call to a node, as an operator reads it, on one line. The first
// *connect.Error in err loses connect's code prefix: one of the node keeps the node's message, and
// one of the client, such as a failure to reach the node, says what failed without net/http's
// request line. A hint follows by code: where the node could not be reached, whether chalkd runs
// there; for a permission refusal, the role the credentials grant; for a method the node does not
// implement, that the two programs may be of different releases. err stays in the error's tree,
// so its code and reason still read. Any other error is returned as it is.
func Describe(err error, h Hints) error {
	ce, ok := errors.AsType[*connect.Error](err)
	if !ok {
		return err
	}
	text := ce.Message()
	if !ce.IsRemote() {
		text = clientCause(ce)
	}
	text += hint(ce, h)
	msg := err.Error()
	if !strings.Contains(msg, ce.Error()) {
		return err
	}
	return &described{msg: strings.Replace(msg, ce.Error(), text, 1), err: err}
}

// described is an error with the text Describe gave it.
type described struct {
	msg string
	err error
}

func (d *described) Error() string { return d.msg }

func (d *described) Unwrap() error { return d.err }

// clientCause is what failed of a call that ce, an error of the client, ended: the cause connect
// and net/http wrapped, without their code and request line.
func clientCause(ce *connect.Error) string {
	var err error = ce
	for {
		switch e := err.(type) {
		case *connect.Error:
			// connect gives an error it wraps its cause's text; one with words of its own, such as
			// a call that ran out of time, keeps them.
			cause := e.Unwrap()
			if cause == nil || e.Message() != cause.Error() {
				return e.Message()
			}
			err = cause
		case *url.Error:
			err = e.Err
		default:
			return err.Error()
		}
	}
}

// hint is what Describe adds to ce for its code, after "; ", or nothing.
func hint(ce *connect.Error, h Hints) string {
	switch ce.Code() {
	case connect.CodePermissionDenied:
		if ce.IsRemote() && h.Credentials != "" && h.Role != "" {
			return "; " + h.Credentials + " grants the " + string(h.Role) + " role"
		}
	case connect.CodeUnimplemented:
		if h.Client != "" {
			release := ""
			if h.Release != "" {
				release = ", " + h.Release
			}
			return "; the node's chalkd may belong to another release than this " + h.Client + release + ": use the " + h.Client + " of the node's release"
		}
	}
	if op, ok := errors.AsType[*net.OpError](ce); ok && !ce.IsRemote() && op.Op == "dial" {
		return "; is chalkd running there, and does the address reach it?"
	}
	return ""
}
