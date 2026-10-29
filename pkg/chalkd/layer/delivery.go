package layer

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/trevex/chalkos/pkg/config"
)

// Planned is a delivery as a plugin checks it: ApplyConfig's, or an install's.
type Planned struct {
	// Payloads are the request's payloads the plugin accepts; empty when it carries none of them.
	Payloads []*anypb.Any
	// Identity is the identity the node runs once the delivery is applied (JSON).
	Identity []byte
	// Documents are the config's documents with their defaults; nil when the request keeps the
	// node's config, and for an install.
	Documents config.Documents
	// OSCA is the OS CAs the node trusts once the delivery is applied (PEM).
	OSCA string
	// Trust is set when the request replaces the OS CAs the node trusts.
	Trust bool
	// Install is set for an install, which writes the payloads to the STATE it lays out.
	Install bool
	// ForceRenewals asks the layer to renew its certificates once the delivery is applied, as
	// test images do.
	ForceRenewals bool
}

// Commit is what a planned delivery changes; a nil function changes nothing.
type Commit struct {
	// Config writes what the layer derives from the config, once the identity is applied. An
	// install leaves it unset.
	Config func() error
	// Payloads records the payloads on the STATE mounted at stateDir, after the node certificate.
	Payloads func(stateDir string) error
	// Applied runs once the effects are taken. An install leaves it unset.
	Applied func()
}

// Plan hands each payload to the plugin that accepts it and has every plugin plan the delivery.
// It refuses a payload no plugin accepts before any plugin plans.
func Plan(ctx context.Context, plugins []Delivery, payloads []*anypb.Any, d Planned) ([]Commit, error) {
	accepted := make([][]*anypb.Any, len(plugins))
	for _, a := range payloads {
		claimed := false
		for i, p := range plugins {
			if p.Accepts(a) {
				accepted[i] = append(accepted[i], a)
				claimed = true
				break
			}
		}
		if !claimed {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("the request carries a payload of type %s, which no layer of this image takes", a.GetTypeUrl()))
		}
	}
	var commits []Commit
	for i, p := range plugins {
		pd := d
		pd.Payloads = accepted[i]
		c, err := p.Plan(ctx, pd)
		if err != nil {
			return nil, err
		}
		commits = append(commits, c)
	}
	return commits, nil
}
