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
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/schema"
)

// contract is what an image accepts as config: the role and platform it was built for, the
// schemas of its kinds and its image.json, as files to send a node, and loaded.
type contract struct {
	role, platform string
	files          map[string][]byte
	kubernetes     []byte
	schemas        *schema.Schemas
	// image is the image's Kubernetes facts; nil on an image without Kubernetes.
	image *k8s.Image
}

func newContract(files map[string][]byte, imageJSON []byte) (contract, error) {
	s, err := schema.LoadFiles(files)
	if err != nil {
		return contract{}, err
	}
	c := contract{files: files, kubernetes: imageJSON, schemas: s}
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
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
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
	c.role, c.platform = resp.Msg.Role, resp.Msg.Platform
	return c, nil
}

// delivery is a node's config as chalkctl delivers it, checked against the image it is for, with
// the identity it translates to there.
type delivery struct {
	config.Bridged
	identity manifest.Identity
}

// configFor generates the target's config from the cluster definition and checks that, on the
// image the contract describes, it gives back the node's identity. The node translates config
// for the image it runs, so an image of another role or platform than the definition gives the
// node is refused, rather than checked as one thing and applied as another.
func configFor(t *target, c contract) (delivery, error) {
	if err := sameImage(t, c); err != nil {
		return delivery{}, err
	}
	b, err := config.FromManifest(t.cluster.manifest, t.name)
	if err != nil {
		return delivery{}, err
	}
	id, err := b.Check(t.node.Identity, c.schemas, config.Target{Role: c.role, Platform: c.platform, Image: c.image})
	if err != nil {
		return delivery{}, fmt.Errorf("the config of %s: %w", t.name, err)
	}
	return delivery{Bridged: b, identity: id}, nil
}

// sameImage refuses a contract of an image built for another role or platform than the cluster
// definition gives the target.
func sameImage(t *target, c contract) error {
	const reinstall = "changing a node's role or platform is a reinstall"
	switch {
	case c.role != t.node.Role:
		return fmt.Errorf("%s runs an image of the role %s, but the cluster definition gives it the role %s; %s", t.name, c.role, t.node.Role, reinstall)
	case c.platform != t.node.Platform:
		return fmt.Errorf("%s runs an image built for %s, but the cluster definition declares it on %s; %s", t.name, c.platform, t.node.Platform, reinstall)
	}
	return nil
}
