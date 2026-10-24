package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/inventory"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/rules"
	"github.com/trevex/chalkos/pkg/schema"
)

// contract is what an image accepts as config: the platform it was built for, the schemas of its
// kinds and its image.json, as files to send a node, and loaded.
type contract struct {
	platform string
	files    map[string][]byte
	// kubernetesImage is the image's image.json as the image holds it; empty on an image without
	// Kubernetes.
	kubernetesImage []byte
	schemas         *schema.Schemas
	// image is the image's Kubernetes facts; nil on an image without Kubernetes.
	image *k8s.Image
}

func newContract(files map[string][]byte, imageJSON []byte) (contract, error) {
	s, err := schema.LoadFiles(files)
	if err != nil {
		return contract{}, err
	}
	c := contract{files: files, kubernetesImage: imageJSON, schemas: s}
	if len(imageJSON) > 0 {
		img, err := k8s.ParseImage(imageJSON)
		if err != nil {
			return contract{}, fmt.Errorf("image.json: %w", err)
		}
		c.image = &img
	}
	return c, nil
}

// imageContract reads the contract of an image directory: its schemas/ and image.json.
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
	imageJSON, err := os.ReadFile(filepath.Join(dir, "image.json"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// An image directory of an older chalkos carries its cluster file instead, which no node
		// translates config with any more: read without image.json, the image would pass for one
		// without Kubernetes.
		if _, err := os.Stat(filepath.Join(dir, "kubernetes.json")); err == nil {
			return contract{}, fmt.Errorf("%s has kubernetes.json but no image.json, so it was built by an older chalkos; build the image again with this version", dir)
		}
	case err != nil:
		return contract{}, err
	}
	c, err := newContract(files, imageJSON)
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
	c, err := newContract(resp.Msg.Schemas, resp.Msg.KubernetesImage)
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
	set, id, err := config.Translate(nc.Name, nc.Documents, c.schemas, config.Target{Platform: c.platform, Image: c.image})
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
		d.Object = filled.(map[string]any)
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

// imageContractOf is the contract of the node's image in the inventory, as built for the platform
// its metadata.json gives.
func (c *cluster) imageContractOf(name string) (contract, error) {
	nc, err := c.config(name)
	if err != nil {
		return contract{}, err
	}
	md, err := nc.Resolve()
	if err != nil {
		return contract{}, err
	}
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
	c, err := newContract(a.Contract.Schemas, a.Contract.ImageJSON)
	if err != nil {
		return contract{}, fmt.Errorf("%s: %w", a.Reference, err)
	}
	return c, nil
}
