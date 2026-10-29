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

// Target sets the image's Kubernetes facts, which the cluster is built from at boot.
func (p *Plugin) Target(f *anypb.Any, t *config.Target) (bool, error) {
	img, ok, err := k8s.ImageOfPayload(f)
	if !ok {
		return false, nil
	}
	if err != nil {
		return true, failed(connect.CodeInvalidArgument, "the image's image.json: %v", err)
	}
	if t.Image == nil {
		t.Image = &img
	}
	return true, nil
}
