package layer

import (
	"fmt"

	"connectrpc.com/connect"
)

// ServiceError is err of a call to the service of the layer name. A chalkd that knows the layer
// answers it as unimplemented only on an image without the layer; on a node whose Info names the
// layer, an unimplemented call means a chalkd from before the layers had services of their own,
// and ServiceError says to upgrade it first. Use it only after the node's Info named the layer.
func ServiceError(err error, name string) error {
	if connect.CodeOf(err) == connect.CodeUnimplemented {
		return fmt.Errorf("the node's chalkd serves no API of the %s layer; upgrade chalkd first: %w", name, err)
	}
	return err
}
