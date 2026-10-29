package kubernetes

import (
	"errors"
	"io/fs"
	"os"

	"google.golang.org/protobuf/types/known/anypb"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
)

// FactsPayload is an image's image.json as a contract carries it among its layers' facts.
func FactsPayload(imageJSON []byte) (*anypb.Any, error) {
	return anypb.New(&kubernetesv1.ImageFacts{ImageJson: imageJSON})
}

// ImageOfPayload returns the facts a contract's payload carries, parsed; ok is false for a
// payload of another layer.
func ImageOfPayload(p *anypb.Any) (img Image, ok bool, err error) {
	if !p.MessageIs((*kubernetesv1.ImageFacts)(nil)) {
		return Image{}, false, nil
	}
	var f kubernetesv1.ImageFacts
	if err := p.UnmarshalTo(&f); err != nil {
		return Image{}, true, err
	}
	img, err = ParseImage(f.ImageJson)
	return img, true, err
}

// FactsPayloadOf reads an image.json file as FactsPayload wraps it; nil when the file does not
// exist, as on an image without Kubernetes, or is empty.
func FactsPayloadOf(path string) (*anypb.Any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) || err == nil && len(data) == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return FactsPayload(data)
}
