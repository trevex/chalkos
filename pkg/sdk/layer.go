package sdk

import (
	"time"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/outputs"
	"github.com/trevex/chalkos/pkg/schema"
)

// Layer is a layer's part in the SDK. A caller passes the layers it knows; the SDK finds what each
// adds through the optional interfaces below, by type assertion, and asks every layer passed.
type Layer interface {
	// Name is the layer, as a node's Info names it.
	Name() string
}

// ImageFacts is a layer whose images carry facts config is translated with.
type ImageFacts interface {
	// ContractFacts reads the facts of an image's contract: read takes a file name within the
	// image's directory, as its contract layer holds it. They are nil for an image without them.
	ContractFacts(read func(name string) ([]byte, error)) (*anypb.Any, error)
	// Target adds to t's translators the layer's translation of an image with the facts; claimed
	// is false for facts of another layer.
	Target(facts *anypb.Any, t *config.Target) (claimed bool, err error)
}

// Validators is a layer that links the Go code of its images' validator programs, which run
// in-process wherever config is checked against a node's image, as Check does, so config is refused
// before it reaches the node. The image runs its own programs again, which decide.
type Validators interface {
	// Validators are the layer's validators that an image of the schemas runs, by the names the
	// image gives them; none for an image without the layer's kinds.
	Validators(s *schema.Schemas) []outputs.LinkedValidator
}

// Documents is a layer that filters the documents a node gets.
type Documents interface {
	// NodeDocuments are the documents of a node's files that the node gets, as an
	// inventory.Filter: docs or a new slice, with no document changed.
	NodeDocuments(docs []config.Document) []config.Document
}

// Payloads is a layer that issues a node payloads from the cluster's secrets, such as the
// Kubernetes share.
type Payloads interface {
	// Payloads are the layer's payloads for the node with the documents, issued from the secrets
	// at now; none for a node the layer gives nothing.
	Payloads(s clustersecrets.Secrets, node string, docs []config.Document, now time.Time) ([]*anypb.Any, error)
}

// NodeDocuments are the documents a node gets of docs, after every layer's Documents filter.
func NodeDocuments(layers []Layer, docs []config.Document) []config.Document {
	for _, l := range layers {
		if d, ok := l.(Documents); ok {
			docs = d.NodeDocuments(docs)
		}
	}
	return docs
}

// PayloadsOf are the payloads every layer issues the node with the documents, from the secrets at
// now.
func PayloadsOf(layers []Layer, s clustersecrets.Secrets, node string, docs []config.Document, now time.Time) ([]*anypb.Any, error) {
	var payloads []*anypb.Any
	for _, l := range layers {
		p, ok := l.(Payloads)
		if !ok {
			continue
		}
		got, err := p.Payloads(s, node, docs, now)
		if err != nil {
			return nil, err
		}
		payloads = append(payloads, got...)
	}
	return payloads, nil
}
