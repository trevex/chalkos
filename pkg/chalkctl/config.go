package chalkctl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/anypb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/check"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/config"
	imagecontract "github.com/trevex/chalkos/pkg/contract"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/inventory"
	"github.com/trevex/chalkos/pkg/rules"
)

// contract is what an image accepts as config: the platform it was built for, the schemas of its
// kinds and their rules, and its layers' facts, as payloads to send a node, and loaded.
type contract struct {
	platform string
	// image holds the schemas, whose files a node is sent, and their rules.
	image *imagecontract.Contract
	// facts are the image's layers' facts as the image holds them; none on an image whose layers
	// have none.
	facts []*anypb.Any
	// target is what the facts give config's translation, without the platform.
	target config.Target
}

func newContract(image *imagecontract.Contract, facts []*anypb.Any) (contract, error) {
	c := contract{image: image, facts: facts}
	for _, f := range facts {
		claimed := false
		var err error
		for _, p := range compiled[layer.ImageFacts]() {
			if claimed, err = p.Target(f, &c.target); claimed || err != nil {
				break
			}
		}
		switch {
		case err != nil:
			return contract{}, err
		case !claimed:
			return contract{}, fmt.Errorf("the image's contract carries facts of type %s, which this chalkctl does not know", f.GetTypeUrl())
		}
	}
	// Every node of the image copies the target; clipped, a translator one appends lands in an
	// array of its own.
	c.target.Translators = slices.Clip(c.target.Translators)
	return c, nil
}

// contractFacts are the layer facts of an image's contract, whose files read reads by their names
// in the image's directory; none for an image whose layers have none.
func contractFacts(read func(name string) ([]byte, error)) ([]*anypb.Any, error) {
	var facts []*anypb.Any
	for _, p := range compiled[layer.ImageFacts]() {
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

// imageContract reads the contract of an image directory: its schemas/ and its layers' facts.
func imageContract(dir string) (contract, error) {
	paths, err := filepath.Glob(filepath.Join(dir, imagecontract.SchemasDir, "*.json"))
	if err != nil {
		return contract{}, err
	}
	if len(paths) == 0 {
		return contract{}, fmt.Errorf("%s has no schemas/; build the image with this version of chalkos", dir)
	}
	image, err := imagecontract.Load(dir)
	if err != nil {
		return contract{}, fmt.Errorf("%s: %w", dir, err)
	}
	facts, err := contractFacts(func(name string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, name)) })
	if err != nil {
		return contract{}, err
	}
	c, err := newContract(image, facts)
	if err != nil {
		return contract{}, fmt.Errorf("%s: %w", dir, err)
	}
	return c, nil
}

// schemaContract is the contract of an image of the schemas and layer facts.
func schemaContract(schemas map[string][]byte, facts []*anypb.Any) (contract, error) {
	image, err := imagecontract.FromSchemas(schemas)
	if err != nil {
		return contract{}, err
	}
	return newContract(image, facts)
}

// nodeContract asks a node for the contract of the image it runs.
func nodeContract(ctx context.Context, conn *client.Conn, name string) (contract, error) {
	resp, err := conn.Contract(ctx, connect.NewRequest(&nodev1.ContractRequest{}))
	if err != nil {
		return contract{}, fmt.Errorf("ask %s for the schemas of its image: %w", name, err)
	}
	c, err := schemaContract(resp.Msg.Schemas, resp.Msg.LayerFacts)
	if err != nil {
		return contract{}, fmt.Errorf("the schemas of the image %s runs: %w", name, err)
	}
	c.platform = resp.Msg.Platform
	return c, nil
}

// delivery is a node's config checked against its image, with the identity it translates to and
// its redacted documents with defaults, as the image's rules see them.
type delivery struct {
	config.Encoded
	identity  identity.Identity
	documents config.Redacted
	// unpinned names the image references that have a tag but no digest, which chalkctl warns
	// about.
	unpinned []string
}

// deliveryFor checks a node's config against the image ct describes, as the node would. An image of
// another platform than the node's is an error, since the node translates config for its image's
// platform; the image's name is not compared, as the schemas decide. A config the checks refuse is a
// refusal; a node the inventory lacks is an error.
func (c *cluster) deliveryFor(name string, ct contract) (delivery, error) {
	if err := c.samePlatform(name, ct.platform); err != nil {
		return delivery{}, err
	}
	nc, err := c.config(name)
	if err != nil {
		return delivery{}, err
	}
	d, err := checkNodeConfig(nc, ct)
	if err != nil {
		return delivery{}, &layer.NodeError{Node: name, Err: fmt.Errorf("the config of %s: %w", name, err), Named: true, Refused: true}
	}
	return d, nil
}

// checker checks config as the node would, minus rendering, effects and the validators of
// extensions, which are image programs only the node can run, as are the rules that read facts.
func (c contract) checker() (check.Image, error) {
	set, err := c.image.Rules()
	if err != nil {
		return check.Image{}, fmt.Errorf("the image's rules: %w", err)
	}
	t := c.target
	t.Platform = c.platform
	var linked check.Linked
	for _, p := range compiled[layer.Validators]() {
		linked = append(linked, p.Validators(c.image.Schemas)...)
	}
	return check.Image{Schemas: c.image.Schemas, Target: t, ChangeRules: set, Validators: linked}, nil
}

// checkNodeConfig checks a node's config against the contract as the node would, but without the
// node's current config: rules of fields that read oldSelf do not run and rules of kinds see no
// oldDocuments. It returns the config ready to deliver; a refusal is a *check.Refusal.
func checkNodeConfig(nc *inventory.NodeConfig, c contract) (delivery, error) {
	img, err := c.checker()
	if err != nil {
		return delivery{}, err
	}
	tr, err := img.Translate(nc.Name, nc.Documents)
	if err != nil {
		return delivery{}, err
	}
	docs := config.Redact(tr.Documents)
	if err := img.Rules(rules.Input{Documents: docs, Node: nc.Name}); err != nil {
		return delivery{}, err
	}
	if err := img.Validate(context.Background(), nc.Name, docs); err != nil {
		return delivery{}, err
	}
	e, err := config.Encode(nc.Documents)
	if err != nil {
		return delivery{}, err
	}
	return delivery{Encoded: e, identity: tr.Identity, documents: docs, unpinned: tr.Documents.UnpinnedImages(c.image.Schemas)}, nil
}

// checkRules runs the change rules that read no facts on next against old, the config the node
// serves (empty when unknown, as for validate, so oldSelf rules do not run). old gets the image's
// defaults; node is the inventory's name, which rules read as node.name.
func checkRules(c contract, next, old config.Redacted, node string) error {
	img, err := c.checker()
	if err != nil {
		return err
	}
	return img.Rules(rules.Input{Documents: next, Old: old.Defaulted(img.Schemas), Node: node})
}

// imageContractOf is the contract of the node's image in the inventory.
func (c *cluster) imageContractOf(name string) (contract, error) {
	nc, md, err := c.image(name)
	if err != nil {
		return contract{}, err
	}
	return inventoryContract(nc, md.Platform)
}

// inventoryContract is the contract of a node's image in the inventory, built for platform: that
// of its directory, or of the contract layer of its image in a registry.
func inventoryContract(nc *inventory.NodeConfig, platform string) (contract, error) {
	var c contract
	var err error
	if nc.ImageOCI == "" {
		c, err = imageContract(nc.ImageDir)
	} else {
		c, err = registryContract(nc)
	}
	c.platform = platform
	return c, err
}

// registryContract is the contract of the contract layer of a node's image in a registry.
func registryContract(nc *inventory.NodeConfig) (contract, error) {
	a, err := nc.Artifact()
	if err != nil {
		return contract{}, err
	}
	facts, err := contractFacts(a.Contract.ReadFile)
	if err != nil {
		return contract{}, err
	}
	c, err := schemaContract(a.Contract.Schemas, facts)
	if err != nil {
		return contract{}, fmt.Errorf("%s: %w", a.Reference, err)
	}
	return c, nil
}

// warnUnpinned prints the image references of the node's config that name a tag but no digest,
// which every command that checks or delivers config warns about.
func (a *app) warnUnpinned(name string, d delivery) {
	for _, w := range d.unpinned {
		fmt.Fprintf(a.stderr, "chalkctl: warning: %s: %s\n", name, w)
	}
}
