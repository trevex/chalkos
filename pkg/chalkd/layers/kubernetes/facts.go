package kubernetes

import (
	"errors"
	"fmt"
	"io/fs"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/config"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/translate"
)

var _ layer.ImageFacts = (*Plugin)(nil)

// RunningFacts are the running image's image.json; nil on an image without it.
func (p *Plugin) RunningFacts() (*anypb.Any, error) {
	return k8s.FactsPayloadOf(p.k.imageFile)
}

// SystemFacts read the image.json of an image's system.
func (p *Plugin) SystemFacts(read func(path string) ([]byte, error)) (*anypb.Any, error) {
	return factsOf(read(k8s.ImageFile))
}

// ContractFacts read the image.json of an image's directory.
func (p *Plugin) ContractFacts(read func(name string) ([]byte, error)) (*anypb.Any, error) {
	return factsOf(read("image.json"))
}

// factsOf wraps an image.json that was read; none when it does not exist or is empty.
func factsOf(data []byte, err error) (*anypb.Any, error) {
	if errors.Is(err, fs.ErrNotExist) || err == nil && len(data) == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("the image's Kubernetes facts: %w", err)
	}
	return k8s.FactsPayload(data)
}

// Target adds the translation of an image with the Kubernetes facts f, the facts the cluster is
// built from at boot.
func (p *Plugin) Target(f *anypb.Any, t *config.Target) (bool, error) {
	claimed, err := translate.Target(f, t)
	if err != nil {
		return true, layer.Errorf(connect.CodeInvalidArgument, "the image's image.json: %w", err)
	}
	return claimed, nil
}
