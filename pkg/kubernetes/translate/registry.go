package translate

import (
	"fmt"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/kubernetes/clusterregistry"
)

// ClusterRegistry returns the ClusterRegistry among docs; ok is false without one.
func ClusterRegistry(docs config.Documents) (r clusterregistry.Registry, ok bool, err error) {
	found := docs.Of(API, clusterregistry.Kind)
	if len(found) == 0 {
		return clusterregistry.Registry{}, false, nil
	}
	var spec clusterregistry.Spec
	if err := found[0].DecodeSpec(&spec); err != nil {
		return clusterregistry.Registry{}, false, err
	}
	r, err = clusterregistry.New(spec)
	if err != nil {
		return clusterregistry.Registry{}, false, fmt.Errorf("%s: %w", found[0], err)
	}
	return r, true, nil
}
