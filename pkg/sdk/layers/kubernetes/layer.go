// Package kubernetes is the Kubernetes layer's part in the SDK: the facts its images carry, the
// Addon documents it leaves to the control planes, and the share it issues a node.
package kubernetes

import (
	"errors"
	"fmt"
	"io/fs"
	"time"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/config"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/kubernetes/translate"
	"github.com/trevex/chalkos/pkg/layers"
	"github.com/trevex/chalkos/pkg/sdk"
)

// Layer is the Kubernetes layer.
type Layer struct{}

var (
	_ sdk.Layer      = Layer{}
	_ sdk.ImageFacts = Layer{}
	_ sdk.Documents  = Layer{}
	_ sdk.Payloads   = Layer{}
)

// Name is the Kubernetes layer's.
func (Layer) Name() string { return layers.Kubernetes }

// ContractFacts read the image's Kubernetes facts from its directory's facts/kubernetes.json;
// none when it does not exist or is empty.
func (Layer) ContractFacts(read func(name string) ([]byte, error)) (*anypb.Any, error) {
	data, err := read(k8s.ContractFactsFile)
	if errors.Is(err, fs.ErrNotExist) || err == nil && len(data) == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return k8s.FactsPayload(data)
}

// Target adds the translation of an image with the Kubernetes facts f, which config's cluster is
// built from.
func (Layer) Target(f *anypb.Any, t *config.Target) (bool, error) {
	claimed, err := translate.Target(f, t)
	if err != nil {
		return true, fmt.Errorf("%s: %w", k8s.ContractFactsFile, err)
	}
	return claimed, nil
}

// NodeDocuments leaves the Addon documents to the control planes.
func (Layer) NodeDocuments(docs []config.Document) []config.Document {
	return translate.NodeDocuments(docs)
}

// Payloads are the node's share, issued from the secrets for the type its Node document names;
// none for a node without one.
func (Layer) Payloads(s clustersecrets.Secrets, node string, docs []config.Document, now time.Time) ([]*anypb.Any, error) {
	kind := translate.NodeType(docs)
	if kind == "" {
		return nil, nil
	}
	share, err := kpki.ShareFor(&s, kind, node, now)
	if err != nil {
		return nil, err
	}
	data, err := share.Encode()
	if err != nil {
		return nil, err
	}
	payload, err := kpki.Payload(data)
	if err != nil {
		return nil, err
	}
	return []*anypb.Any{payload}, nil
}
