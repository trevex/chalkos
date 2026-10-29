package kubernetes

import (
	"errors"
	"fmt"
	"io/fs"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/trevex/chalkos/pkg/config"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
)

// ContractFacts read the image.json of an image's directory; none when it does not exist or is
// empty.
func (p *Plugin) ContractFacts(read func(name string) ([]byte, error)) (*anypb.Any, error) {
	data, err := read("image.json")
	if errors.Is(err, fs.ErrNotExist) || err == nil && len(data) == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return k8s.FactsPayload(data)
}

// Target sets the image's Kubernetes facts, which config's cluster is built from.
func (p *Plugin) Target(f *anypb.Any, t *config.Target) (bool, error) {
	img, ok, err := k8s.ImageOfPayload(f)
	if !ok {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("image.json: %w", err)
	}
	if t.Image == nil {
		t.Image = &img
	}
	return true, nil
}
