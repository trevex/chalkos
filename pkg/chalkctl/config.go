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

// contract is what an image accepts as config: the schemas of its kinds and its kubernetes.json,
// as files to send an installer, and loaded.
type contract struct {
	files      map[string][]byte
	kubernetes []byte
	schemas    *schema.Schemas
	cluster    *k8s.Cluster
}

func newContract(files map[string][]byte, kubernetesJSON []byte) (contract, error) {
	s, err := schema.LoadFiles(files)
	if err != nil {
		return contract{}, err
	}
	c := contract{files: files, kubernetes: kubernetesJSON, schemas: s}
	if len(kubernetesJSON) > 0 {
		k, err := k8s.ParseCluster(kubernetesJSON)
		if err != nil {
			return contract{}, fmt.Errorf("kubernetes.json: %w", err)
		}
		c.cluster = &k
	}
	return c, nil
}

// imageContract reads the contract of an image directory: its schemas/ and kubernetes.json.
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
	kubernetesJSON, err := os.ReadFile(filepath.Join(dir, "kubernetes.json"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return contract{}, err
	}
	c, err := newContract(files, kubernetesJSON)
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
	c, err := newContract(resp.Msg.Schemas, resp.Msg.KubernetesCluster)
	if err != nil {
		return contract{}, fmt.Errorf("the schemas of the image %s runs: %w", name, err)
	}
	return c, nil
}

// delivery is a node's config as chalkctl delivers it, checked against the image it is for, with
// the identity it translates to there.
type delivery struct {
	config.Bridged
	identity manifest.Identity
}

// configFor generates the target's config from the cluster definition and checks that, on the
// image the contract describes, it gives back the node's identity.
func configFor(t *target, c contract) (delivery, error) {
	b, err := config.FromManifest(t.cluster.manifest, t.name)
	if err != nil {
		return delivery{}, err
	}
	id, err := b.Check(t.node.Identity, c.schemas, config.Target{Role: t.node.Role, Platform: t.node.Platform, Kubernetes: c.cluster})
	if err != nil {
		return delivery{}, fmt.Errorf("the config of %s: %w", t.name, err)
	}
	return delivery{Bridged: b, identity: id}, nil
}
