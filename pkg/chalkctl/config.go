package chalkctl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/config"
	imagecontract "github.com/trevex/chalkos/pkg/contract"
	"github.com/trevex/chalkos/pkg/inventory"
	"github.com/trevex/chalkos/pkg/sdk"
)

// imageContract reads the contract of an image directory: its schemas/ and its layers' facts.
func imageContract(dir string) (sdk.Contract, error) {
	paths, err := filepath.Glob(filepath.Join(dir, imagecontract.SchemasDir, "*.json"))
	if err != nil {
		return sdk.Contract{}, err
	}
	if len(paths) == 0 {
		return sdk.Contract{}, fmt.Errorf("%s has no schemas/; build the image with this version of chalkos", dir)
	}
	image, err := imagecontract.Load(dir)
	if err != nil {
		return sdk.Contract{}, fmt.Errorf("%s: %w", dir, err)
	}
	facts, err := sdk.ContractFacts(sdkLayers(), func(name string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, name)) })
	if err != nil {
		return sdk.Contract{}, err
	}
	c, err := sdk.NewContract(sdkLayers(), image, facts)
	if err != nil {
		return sdk.Contract{}, fmt.Errorf("%s: %w", dir, err)
	}
	return c, nil
}

// deliveryFor checks a node's config against the image ct describes, as the node would. An image of
// another platform than the node's is an error, since the node translates config for its image's
// platform; the image's name is not compared, as the schemas decide. A config the checks refuse is
// a refusal; a node the inventory lacks is an error. It returns the documents it checked too, with
// the values of their Secrets resolved.
func (c *cluster) deliveryFor(ctx context.Context, name string, ct sdk.Contract) (sdk.Delivery, []config.Document, error) {
	if err := c.samePlatform(name, ct.Platform); err != nil {
		return sdk.Delivery{}, nil, err
	}
	nc, err := c.desiredConfig(name)
	if err != nil {
		return sdk.Delivery{}, nil, err
	}
	d, err := sdk.Check(ctx, ct, nc.Name, nc.Documents)
	if err != nil {
		return sdk.Delivery{}, nil, &layer.NodeError{Node: name, Err: fmt.Errorf("the config of %s: %w", name, err), Named: true, Refused: true}
	}
	return d, nc.Documents, nil
}

// imageContractOf is the contract of the node's image in the inventory.
func (c *cluster) imageContractOf(name string) (sdk.Contract, error) {
	nc, md, err := c.image(name)
	if err != nil {
		return sdk.Contract{}, err
	}
	return inventoryContract(nc, md.Platform)
}

// inventoryContract is the contract of a node's image in the inventory, built for platform: that
// of its directory, or of the contract layer of its image in a registry.
func inventoryContract(nc *inventory.NodeConfig, platform string) (sdk.Contract, error) {
	var c sdk.Contract
	var err error
	if nc.ImageOCI == "" {
		c, err = imageContract(nc.ImageDir)
	} else {
		c, err = registryContract(nc)
	}
	c.Platform = platform
	return c, err
}

// registryContract is the contract of the contract layer of a node's image in a registry.
func registryContract(nc *inventory.NodeConfig) (sdk.Contract, error) {
	a, err := nc.Artifact()
	if err != nil {
		return sdk.Contract{}, err
	}
	return sdk.ArtifactContract(sdkLayers(), a)
}

// warnUnpinned prints the image references of the node's config that name a tag but no digest,
// which every command that checks or delivers config warns about.
func (a *app) warnUnpinned(name string, d sdk.Delivery) {
	for _, w := range d.Unpinned {
		fmt.Fprintf(a.stderr, "chalkctl: warning: %s: %s\n", name, w)
	}
}
