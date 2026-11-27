package sdk

import (
	"context"
	"fmt"
	"slices"

	"google.golang.org/protobuf/types/known/anypb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/check"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/config"
	imagecontract "github.com/trevex/chalkos/pkg/contract"
	"github.com/trevex/chalkos/pkg/ociimage"
)

// Contract is what an image accepts as config: the platform it was built for, the schemas of its
// kinds and their rules, and its layers' facts, as payloads to send a node, and loaded. It is
// built with NewContract, SchemaContract or NodeContract, which give it the layers that link their
// validators; a Contract literal links none.
type Contract struct {
	// Platform is the platform the image was built for, which config is translated for.
	Platform string
	// Image holds the schemas, whose files a node is sent, and their rules.
	Image *imagecontract.Contract
	// Facts are the image's layers' facts as the image holds them; none on an image whose layers
	// have none.
	Facts []*anypb.Any
	// target is what the facts give config's translation, without the platform.
	target config.Target
	// layers are the layers NewContract was given, which link their validators to the checks.
	layers []Layer
}

// NewContract is the contract of the image with the layer facts, which one of the layers must
// claim each of. The contract keeps the layers for its checks.
func NewContract(layers []Layer, image *imagecontract.Contract, facts []*anypb.Any) (Contract, error) {
	c := Contract{Image: image, Facts: facts, layers: layers}
	for _, f := range facts {
		claimed := false
		var err error
		for _, l := range layers {
			p, ok := l.(ImageFacts)
			if !ok {
				continue
			}
			if claimed, err = p.Target(f, &c.target); claimed || err != nil {
				break
			}
		}
		switch {
		case err != nil:
			return Contract{}, err
		case !claimed:
			return Contract{}, fmt.Errorf("the image's contract carries facts of type %s, which none of the layers given claims", f.GetTypeUrl())
		}
	}
	// Every node of the image copies the target; clipped, a translator one appends lands in an
	// array of its own.
	c.target.Translators = slices.Clip(c.target.Translators)
	return c, nil
}

// ContractFacts are the layer facts of an image's contract, whose files read reads by their names
// in the image's directory; none for an image whose layers have none.
func ContractFacts(layers []Layer, read func(name string) ([]byte, error)) ([]*anypb.Any, error) {
	var facts []*anypb.Any
	for _, l := range layers {
		p, ok := l.(ImageFacts)
		if !ok {
			continue
		}
		f, err := p.ContractFacts(read)
		if err != nil {
			return nil, err
		}
		if f != nil {
			facts = append(facts, f)
		}
	}
	return facts, nil
}

// SchemaContract is the contract of an image of the schemas and layer facts.
func SchemaContract(layers []Layer, schemas map[string][]byte, facts []*anypb.Any) (Contract, error) {
	image, err := imagecontract.FromSchemas(schemas)
	if err != nil {
		return Contract{}, err
	}
	return NewContract(layers, image, facts)
}

// NodeContract asks the node conn reaches for the contract of the image it runs; name is how its
// errors call the node.
func NodeContract(ctx context.Context, conn *client.Conn, layers []Layer, name string) (Contract, error) {
	resp, err := conn.Contract(ctx, &nodev1.ContractRequest{})
	if err != nil {
		return Contract{}, fmt.Errorf("ask %s for the schemas of its image: %w", name, err)
	}
	c, err := SchemaContract(layers, resp.Schemas, resp.LayerFacts)
	if err != nil {
		return Contract{}, fmt.Errorf("the schemas of the image %s runs: %w", name, err)
	}
	c.Platform = resp.Platform
	return c, nil
}

// Checker checks config as the node would, minus rendering, effects and the validators of
// extensions, which are image programs only the node can run, as are the rules that read facts.
// The contract's layers link their validators.
func (c Contract) Checker() (check.Image, error) {
	set, err := c.Image.Rules()
	if err != nil {
		return check.Image{}, fmt.Errorf("the image's rules: %w", err)
	}
	t := c.target
	t.Platform = c.Platform
	var linked check.Linked
	for _, l := range c.layers {
		if p, ok := l.(Validators); ok {
			linked = append(linked, p.Validators(c.Image.Schemas)...)
		}
	}
	return check.Image{Schemas: c.Image.Schemas, Target: t, ChangeRules: set, Validators: linked}, nil
}

// ArtifactContract is the contract of the contract layer of an image in a registry, built for the
// platform its header names.
func ArtifactContract(layers []Layer, a *ociimage.Artifact) (Contract, error) {
	facts, err := ContractFacts(layers, a.Contract.ReadFile)
	if err != nil {
		return Contract{}, err
	}
	c, err := SchemaContract(layers, a.Contract.Schemas, facts)
	if err != nil {
		return Contract{}, fmt.Errorf("%s: %w", a.Reference, err)
	}
	c.Platform = a.Header.Platform
	return c, nil
}
