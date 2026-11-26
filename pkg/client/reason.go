package client

import (
	"strings"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectproto"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
)

// reasonPrefix starts the name of every nodev1.ErrorReason, which ReasonName leaves out.
const reasonPrefix = "ERROR_REASON_"

// Reason is the reason chalkd gave for err: the first in err's tree of the nodev1.ReasonDetail of a
// node's answer and, within chalkd, the reason an error gives before chalkd answers with it, by an
// ErrorReason method that names it as ReasonName does. It is unspecified for an error without one.
func Reason(err error) nodev1.ErrorReason {
	for _, e := range tree(err) {
		if r, ok := e.(interface{ ErrorReason() string }); ok {
			if reason := ReasonNamed(r.ErrorReason()); reason != nodev1.ErrorReason_ERROR_REASON_UNSPECIFIED {
				return reason
			}
		}
		d, ok := e.(interface{ Details() []*connect.ErrorDetail })
		if !ok {
			continue
		}
		for _, detail := range d.Details() {
			if v, err := connectproto.UnmarshalErrorDetail(detail); err == nil {
				if r, ok := v.(*nodev1.ReasonDetail); ok {
					return r.GetReason()
				}
			}
		}
	}
	return nodev1.ErrorReason_ERROR_REASON_UNSPECIFIED
}

// ReasonName is reason's name without its ERROR_REASON_ prefix, as a change rule names it, such as
// NO_FALLBACK_SECRET; empty for no reason.
func ReasonName(reason nodev1.ErrorReason) string {
	if reason == nodev1.ErrorReason_ERROR_REASON_UNSPECIFIED {
		return ""
	}
	return strings.TrimPrefix(reason.String(), reasonPrefix)
}

// ReasonNamed is the reason ReasonName names name; unspecified for a name the node API does not
// know.
func ReasonNamed(name string) nodev1.ErrorReason {
	if name == "" {
		return nodev1.ErrorReason_ERROR_REASON_UNSPECIFIED
	}
	return nodev1.ErrorReason(nodev1.ErrorReason_value[reasonPrefix+name])
}

// tree is err and the errors it wraps, depth first.
func tree(err error) []error {
	if err == nil {
		return nil
	}
	all := []error{err}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		all = append(all, tree(u.Unwrap())...)
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			all = append(all, tree(e)...)
		}
	}
	return all
}
