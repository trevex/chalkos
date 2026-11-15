package derive

import (
	"errors"
	"fmt"

	"github.com/trevex/chalkos/pkg/refusals"
)

// internalError is a guard's error. The schemas, rules and validators refuse its cause before
// chalkd renders; the guard stays because an output would otherwise panic, carry syntax a value
// adds, or fail to load. It means a bug in chalkd or its input, not in the config.
type internalError struct{ text string }

func (e *internalError) Error() string { return "internal error: " + e.text }

// InternalErrorf is the internal error of the message format gives, which may wrap with %w.
func InternalErrorf(format string, args ...any) error {
	return &internalError{fmt.Errorf(format, args...).Error() + "; chalkd checks the config against the schemas, the rules and the validators before it renders"}
}

// isInternal reports whether err holds an error that is no refusal, such as a guard's, which ends
// a check that goes on past its refusals.
func isInternal(err error) bool {
	_, others := refusals.Split(err)
	return len(others) > 0
}

// Within prefixes err with what. A refusal already names its document and stays as it is; an
// internal error keeps "internal error:" in front.
func Within(what string, err error) error {
	if _, ok := errors.AsType[*refusals.Error](err); ok {
		return err
	}
	if e, ok := err.(*internalError); ok {
		return &internalError{what + ": " + e.text}
	}
	return fmt.Errorf("%s: %w", what, err)
}

// MustGet is the value byName holds for the document of the kind that a reference names. chalkd
// checks every reference a kind's schema marks before it renders an output, so a name byName
// lacks is a bug of chalkd or of the input, not an error of the config.
func MustGet[V any](byName map[string]V, kind, name string) (V, error) {
	v, ok := byName[name]
	if !ok {
		return v, &internalError{fmt.Sprintf("the input holds no %s %q, which a reference names; chalkd checks references before it renders", kind, name)}
	}
	return v, nil
}
