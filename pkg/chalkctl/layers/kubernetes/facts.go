package kubernetes

import (
	"errors"
	"fmt"
	"io/fs"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/trevex/chalkos/pkg/config"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/translate"
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

// Target adds the translation of an image with the Kubernetes facts f, which config's cluster is
// built from.
func (p *Plugin) Target(f *anypb.Any, t *config.Target) (bool, error) {
	claimed, err := translate.Target(f, t)
	if err != nil {
		return true, fmt.Errorf("image.json: %w", err)
	}
	return claimed, nil
}
