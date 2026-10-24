package chalkd

import (
	"encoding/json"

	"connectrpc.com/connect"

	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/kubernetes/nodeip"
	"github.com/trevex/chalkos/pkg/storage"
)

// delivered is an identity as it arrives, with what chalkd reads from it.
type delivered struct {
	data       []byte
	section    storage.Section
	kubernetes *identity.Kubernetes
}

func parseIdentity(data string) (delivered, error) {
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
	// A node that cannot read how to pick its address would run no kubelet.
	if k := id.Kubernetes; k != nil {
		if err := k.CheckNodeIPs(); err != nil {
			return delivered{}, failed(connect.CodeInvalidArgument, "the identity's %v", err)
		}
		if _, err := nodeip.ParseFilter(k.ValidSubnets); err != nil {
			return delivered{}, failed(connect.CodeInvalidArgument, "%v", err)
		}
	}
	return delivered{data: []byte(data), section: id.Storage, kubernetes: id.Kubernetes}, nil
}
