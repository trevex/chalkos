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
// the identity it translates to there.
type delivery struct {
	config.Encoded
	identity identity.Identity
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

// checkNodeConfig checks a node's config as the node checks it on the image the contract
// describes: the set's rules, each document against its schema, the identity it translates
// into, and on an image with Kubernetes the cluster it gives. It returns the config as chalkctl
// delivers it.
func checkNodeConfig(nc *inventory.NodeConfig, c contract) (delivery, error) {
	set, err := config.NewSet(nc.Documents, c.schemas)
	if err != nil {
		return delivery{}, err
	}
	id, err := set.Identity(config.Target{Platform: c.platform, Image: c.image})
	if err != nil {
		return delivery{}, err
	}
	if c.image != nil {
		if err := set.CheckKubernetes(*c.image); err != nil {
			return delivery{}, err
		}
	}
	e, err := config.Encode(nc.Documents)
	if err != nil {
		return delivery{}, err
	}
	return delivery{Encoded: e, identity: id}, nil
}

// imageContractOf is the contract of the image a node's Image names, as built for the platform its
// directory's metadata.json gives.
func (c *cluster) imageContractOf(name string) (contract, error) {
	nc, err := c.config(name)
	if err != nil {
		return contract{}, err
	}
	md, err := nc.Resolve()
	if err != nil {
		return contract{}, err
	}
	ct, err := imageContract(nc.ImageDir)
	if err != nil {
		return contract{}, err
	}
	ct.platform = md.Platform
	return ct, nil
}
