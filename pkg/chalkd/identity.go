package chalkd

import (
	"encoding/json"

	"connectrpc.com/connect"

	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/storage"
)

// delivered is an identity as it arrives, with what chalkd reads from it.
type delivered struct {
	data    []byte
	section storage.Section
}

// parseIdentity reads an identity and has every compiled layer check its part of it, a check that
// passes without that part: the identity may be for another image, as an upgrade's slot is.
func (s *Server) parseIdentity(data string) (delivered, error) {
	var id identity.Identity
	if err := json.Unmarshal([]byte(data), &id); err != nil {
		return delivered{}, failed(connect.CodeInvalidArgument, "parse the identity: %v", err)
	}
	if _, ok := id.Storage.Disks[storage.SystemDisk]; !ok {
		return delivered{}, failed(connect.CodeInvalidArgument, "the identity's storage section has no system disk")
	}
	if err := id.Storage.Validate(); err != nil {
		return delivered{}, failed(connect.CodeInvalidArgument, "%v", err)
	}
	for _, p := range layer.Compiled[layer.Delivery](s.Plugins) {
		if err := p.CheckIdentity([]byte(data)); err != nil {
			return delivered{}, err
		}
	}
	return delivered{data: []byte(data), section: id.Storage}, nil
}
