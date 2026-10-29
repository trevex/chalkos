package pki

import (
	"google.golang.org/protobuf/types/known/anypb"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
)

// Payload is an encoded share as ApplyConfig and Install deliver it among their layers.
func Payload(share []byte) (*anypb.Any, error) {
	return anypb.New(&kubernetesv1.Share{Share: share})
}

// IsPayload reports whether a payload is a share.
func IsPayload(p *anypb.Any) bool { return p.MessageIs((*kubernetesv1.Share)(nil)) }

// SharePayload returns the share a payload carries, as delivered.
func SharePayload(p *anypb.Any) ([]byte, error) {
	var s kubernetesv1.Share
	if err := p.UnmarshalTo(&s); err != nil {
		return nil, err
	}
	return s.Share, nil
}
