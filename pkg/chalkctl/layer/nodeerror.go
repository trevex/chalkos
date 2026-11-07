package layer

import (
	"errors"
	"strings"
)

// NodeError is an error about the node Node. Its message names the node once: Err's message, with
// the node's name in place of <node>, which the node's own messages say, after the name unless
// Named. The error it wraps stays reachable, so its connect code still decides whether another
// node is tried.
type NodeError struct {
	Node string
	Err  error
	// Named is set when Err's message names the node already.
	Named bool
	// Refused is set when the error refuses the node's config: chalkctl's checks of it against the
	// image's schemas and rules, or the node's. A dry run counts it apart from a failure before the
	// config is checked, as of a node unreachable or on trial, which refuses nothing.
	Refused bool
}

func (e *NodeError) Error() string {
	msg := strings.ReplaceAll(e.Err.Error(), "<node>", e.Node)
	if e.Named {
		return msg
	}
	return e.Node + ": " + msg
}

func (e *NodeError) Unwrap() error { return e.Err }

// Named is err as an error about the node called name: err as it is if it is nil or names the
// node already, a NodeError of it otherwise.
func Named(name string, err error) error {
	if err == nil {
		return nil
	}
	if e, ok := errors.AsType[*NodeError](err); ok && e.Node == name {
		return err
	}
	return &NodeError{Node: name, Err: err}
}

// Unnamed is err without the name a NodeError about the node called name puts first, for a
// message about the node that names it before err.
func Unnamed(name string, err error) error {
	if e, ok := err.(*NodeError); ok && e.Node == name && !e.Named {
		return e.Err
	}
	return err
}
