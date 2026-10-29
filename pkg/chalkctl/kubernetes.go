package chalkctl

import (
	"time"

	"google.golang.org/protobuf/types/known/anypb"

	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

// kubernetesShare issues the share of a node with Kubernetes; nil for one without.
func kubernetesShare(t *target, now time.Time) ([]byte, error) {
	kind, err := t.cluster.kind(t.name)
	if err != nil || kind == "" {
		return nil, err
	}
	share, err := kpki.ShareFor(&t.secrets, kind, t.name, now)
	if err != nil {
		return nil, err
	}
	return share.Encode()
}

// sharePayloads are the layer payloads that deliver a share; none for no share, as a node without
// Kubernetes gets.
func sharePayloads(share []byte) ([]*anypb.Any, error) {
	if share == nil {
		return nil, nil
	}
	p, err := kpki.Payload(share)
	if err != nil {
		return nil, err
	}
	return []*anypb.Any{p}, nil
}
