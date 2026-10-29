package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/anypb"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/inventory"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

// contract is what an image accepts as config: the platform it was built for, the schemas of its
// kinds and its layers' facts, as files and payloads to send a node, and loaded.
type contract struct {
	platform string
	files    map[string][]byte
	// facts are the image's layers' facts as the image holds them; none on an image whose layers
	// have none.
	facts   []*anypb.Any
	schemas *schema.Schemas
	// target is what the facts give config's translation, without the platform.
	target config.Target
}

func newContract(files map[string][]byte, facts []*anypb.Any) (contract, error) {
	s, err := schema.LoadFiles(files)
	if err != nil {
		return contract{}, err
	}
	c := contract{files: files, facts: facts, schemas: s}
	for _, f := range facts {
		claimed := false
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
	paths, err := filepath.Glob(filepath.Join(dir, "schemas", "*.json"))
	if err != nil {
		return contract{}, err
	}
	if len(paths) == 0 {
		return contract{}, fmt.Errorf("%s has no schemas/; build the image with this version of chalkos", dir)
	}
	files := map[string][]byte{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return contract{}, err
		}
		files[filepath.Base(p)] = data
	}
	// An image directory of an older chalkos carries its cluster file instead of image.json, which
	// no node translates config with any more: read without image.json, the image would pass for
	// one without Kubernetes.
	if _, err := os.Stat(filepath.Join(dir, "image.json")); errors.Is(err, fs.ErrNotExist) {
		if _, err := os.Stat(filepath.Join(dir, "kubernetes.json")); err == nil {
			return contract{}, fmt.Errorf("%s has kubernetes.json but no image.json, so it was built by an older chalkos; build the image again with this version", dir)
		}
	}
	facts, err := contractFacts(func(name string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, name)) })
	if err != nil {
		return contract{}, err
	}
	c, err := newContract(files, facts)
	if err != nil {
		return contract{}, fmt.Errorf("%s: %w", dir, err)
	}
	return c, nil
}

// nodeContract asks a node for the contract of the image it runs.
func nodeContract(ctx context.Context, conn *client.Conn, name string) (contract, error) {
	resp, err := conn.Contract(ctx, connect.NewRequest(&nodev1.ContractRequest{}))
	if err != nil {
		return contract{}, fmt.Errorf("ask %s for the schemas of its image: %w", name, err)
	}
	c, err := newContract(resp.Msg.Schemas, resp.Msg.LayerFacts)
	if err != nil {
		return contract{}, fmt.Errorf("the schemas of the image %s runs: %w", name, err)
	}
	c.platform = resp.Msg.Platform
	return c, nil
}

// delivery is a node's config as chalkctl delivers it, checked against the image it is for, with
// the identity it translates to there and its documents with their defaults, as the image's rules
// see them.
type delivery struct {
	config.Encoded
	identity  identity.Identity
	documents []config.Document
}

// deliveryFor is a node's config as chalkctl delivers it for the image the contract describes,
// checked as the node checks it. The node translates config for the platform its image is built
// for, so an image of another platform than the node's is refused, rather than checked as one
// thing and applied as another. The image's variant is not compared: the image's schemas decide
// which config it takes.
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
		return delivery{}, fmt.Errorf("the config of %s: %w", name, err)
	}
	return d, nil
}

// checkNodeConfig checks a node's config on the image the contract describes as the node does,
// with config.Translate, and against the image's change rules that chalkctl can check without
// the node's config. It returns the config as chalkctl delivers it.
func checkNodeConfig(nc *inventory.NodeConfig, c contract) (delivery, error) {
	t := c.target
	t.Platform = c.platform
	set, id, err := config.Translate(nc.Name, nc.Documents, c.schemas, t)
	if err != nil {
		return delivery{}, err
	}
	if err := checkRules(c, set.Documents(), nil, nc.Name); err != nil {
		return delivery{}, err
	}
	e, err := config.Encode(nc.Documents)
	if err != nil {
		return delivery{}, err
	}
	return delivery{Encoded: e, identity: id, documents: set.Documents()}, nil
}

// checkRules refuses next, a node's config with its defaults, when the change rules of the image
// the contract describes refuse it, as the node would: those that read no facts, which the node
// alone has. old is the node's config as it serves it, its secret values hashed, or nil where
// chalkctl does not have it, as for validate; without it, the rules of fields that read oldSelf
// do not run and the rules of kinds see no oldDocuments. Documents of old that the image does not
// accept compare with nothing. node is the inventory's name of the node, which the rules read as
// node.name.
func checkRules(c contract, next, old []config.Document, node string) error {
	set, err := rules.Compile(c.schemas)
	if err != nil {
		return fmt.Errorf("the image's rules: %w", err)
	}
	var baseline []config.Document
	for _, d := range old {
		filled, err := c.schemas.Default(d.Object)
		if err != nil {
			continue
		}
		d.Object = filled
		baseline = append(baseline, d)
	}
	failures, err := set.Evaluate(rules.Input{Documents: next, Old: baseline, OldHashed: true, Node: node})
	if err != nil {
		return err
	}
	if len(failures) > 0 {
		return errors.New(rules.Refusal(failures, node))
	}
	return nil
}

// imageContractOf is the contract of the node's image in the inventory.
func (c *cluster) imageContractOf(name string) (contract, error) {
	nc, err := c.config(name)
	if err != nil {
		return contract{}, err
	}
	md, err := nc.Resolve()
	if err != nil {
		return contract{}, err
	}
	return nodeImageContract(nc, md)
}

// nodeImageContract is the contract of a node's image in the inventory, as built for the platform
// md, the image's metadata, gives.
func nodeImageContract(nc *inventory.NodeConfig, md schema.Metadata) (contract, error) {
	ct, err := inventoryContract(nc)
	if err != nil {
		return contract{}, err
	}
	ct.platform = md.Platform
	return ct, nil
}

// inventoryContract is the contract of a node's image in the inventory: that of its directory,
// or of the contract layer of its image in a registry.
func inventoryContract(nc *inventory.NodeConfig) (contract, error) {
	if nc.ImageOCI == "" {
		return imageContract(nc.ImageDir)
	}
	a, err := nc.Artifact()
	if err != nil {
		return contract{}, err
	}
	facts, err := contractFacts(a.Contract.ReadFile)
	if err != nil {
		return contract{}, err
	}
	c, err := newContract(a.Contract.Schemas, facts)
	if err != nil {
		return contract{}, fmt.Errorf("%s: %w", a.Reference, err)
	}
	return c, nil
}
