package layer

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

// ServiceError says to upgrade a chalkd that serves no API of the layer, and passes other errors
// on as they are.
func TestServiceError(t *testing.T) {
	unimplemented := connect.NewError(connect.CodeUnimplemented, errors.New("404 Not Found"))
	err := ServiceError(unimplemented, "router")
	if want := "the node's chalkd serves no API of the router layer; upgrade chalkd first: "; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("unimplemented: %v, want %q", err, want)
	}
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("unimplemented: code %v, want it kept", connect.CodeOf(err))
	}
	denied := connect.NewError(connect.CodePermissionDenied, errors.New("no"))
	if got := ServiceError(denied, "router"); got != denied {
		t.Errorf("denied: %v, want it as it is", got)
	}
	if got := ServiceError(nil, "router"); got != nil {
		t.Errorf("nil: %v", got)
	}
}
