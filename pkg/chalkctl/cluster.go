package chalkctl

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/inventory"
	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/schema"
	"github.com/trevex/chalkos/pkg/sdk"
)

// cluster is the inventory chalkctl operates on, named as the inventory. Without one it is partial
// and nameless: each command names its node and --address reaches it.
type cluster struct {
	name string
	inv  *inventory.Inventory
	// configs holds each node's config, once read from the inventory.
	configs map[string]*inventory.NodeConfig
	// contracts holds the contract of each node's image, once read from the inventory.
	contracts map[string]sdk.Contract
	sources   *secretSources
}

// Partial reports whether the cluster comes without an inventory.
func (c *cluster) Partial() bool { return c.inv == nil }

// loadCluster reads the inventory --inventory or $CHALKOSINVENTORY names, or else ./chalkos.yaml;
// without any the cluster is partial.
func loadCluster(ctx context.Context, f layer.ClusterFlags) (*cluster, error) {
	if path := f.Path(); path != "" {
		return loadInventory(ctx, path)
	}
	switch _, err := os.Stat(inventory.DefaultFile); {
	case err == nil:
		return loadInventory(ctx, inventory.DefaultFile)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	return &cluster{}, nil
}

// loadInventory reads and checks the inventory at path. A compiled layer may refuse its nodes, such
// as nodes with different Cluster, Flannel or ClusterRegistry specs.
func loadInventory(ctx context.Context, path string) (*cluster, error) {
	c, err := readInventory(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := c.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// readInventory reads the inventory at path, each node's documents filtered by the compiled
// layers, without checking it.
func readInventory(ctx context.Context, path string) (*cluster, error) {
	ls := sdkLayers()
	inv, err := inventory.Load(path, func(docs []config.Document) []config.Document { return sdk.NodeDocuments(ls, docs) })
	if err != nil {
		return nil, err
	}
	inv.SetContext(ctx)
	return &cluster{name: inv.Name, inv: inv, configs: map[string]*inventory.NodeConfig{}, contracts: map[string]sdk.Contract{}, sources: sourcesIn(ctx)}, nil
}

// check refuses the inventory's nodes when a compiled layer refuses them together.
func (c *cluster) check() error {
	for _, p := range compiled[layer.Inventory]() {
		if err := p.CheckInventory(c); err != nil {
			return err
		}
	}
	return nil
}

// RequireNodes refuses a partial cluster for a command that needs the nodes' config.
func (c *cluster) RequireNodes(command string) error {
	if c.Partial() {
		return fmt.Errorf("%s needs an inventory, which gives the cluster's nodes and their config; pass --inventory", command)
	}
	return nil
}

// NodeNames are the inventory's nodes, sorted; none on a partial cluster.
func (c *cluster) NodeNames() []string {
	if c.Partial() {
		return nil
	}
	return c.inv.NodeNames()
}

// node refuses a node the cluster does not have. A partial cluster has every node a command
// names.
func (c *cluster) node(name string) error {
	if c.Partial() || slices.Contains(c.NodeNames(), name) {
		return nil
	}
	return fmt.Errorf("the cluster %s has no node %s; its nodes are %s", c.name, name, strings.Join(c.NodeNames(), ", "))
}

// config is a node's config as the inventory gives it, read once.
func (c *cluster) config(name string) (*inventory.NodeConfig, error) {
	if nc, ok := c.configs[name]; ok {
		return nc, nil
	}
	nc, err := c.inv.Node(name)
	if err != nil {
		return nil, err
	}
	c.configs[name] = nc
	return nc, nil
}

// desiredConfig resolves sources when a command needs the actual desired values.
func (c *cluster) desiredConfig(name string) (*inventory.NodeConfig, error) {
	nc, err := c.config(name)
	if err != nil {
		return nil, err
	}
	return c.sources.materialize(nc)
}

// image is the node's config in the inventory and the metadata of its image there.
func (c *cluster) image(name string) (*inventory.NodeConfig, schema.Metadata, error) {
	nc, err := c.config(name)
	if err != nil {
		return nil, schema.Metadata{}, err
	}
	md, err := nc.Resolve()
	return nc, md, err
}

// inventoryImage is the config of the node in the inventory, which names the node's image: its
// directory or its reference in a registry.
func (c *cluster) inventoryImage(name string) (*inventory.NodeConfig, error) {
	if c.Partial() {
		return nil, errors.New("the image comes from --image without an inventory, which names the nodes' images; pass --inventory")
	}
	return c.config(name)
}

// sameBuild refuses an image taken from the directory of a node's image whose store has another
// root hash than the directory's metadata.json names.
func (c *cluster) sameBuild(name string, img *diskImage) error {
	nc, md, err := c.image(name)
	if err != nil {
		return err
	}
	if got := hex.EncodeToString(img.Store.RootHash); got != md.RootHash {
		return fmt.Errorf("%s: metadata.json names the root hash %s, but the image's store has %s; rebuild the image or link its directory again", nc.ImageDir, md.RootHash, got)
	}
	return nil
}

// address returns where a node's chalkd is reached and where that address comes from: --address,
// else the inventory's address of the node.
func (c *cluster) address(flagValue, name string) (addr, source string, err error) {
	if flagValue != "" {
		return flagValue, "--address", nil
	}
	if c.Partial() {
		return "", "", fmt.Errorf("no inventory names the address of %s; pass --address, or --inventory", name)
	}
	if a := c.inv.Spec.Nodes[name].Address; a != "" {
		return a, "the inventory " + c.inv.Path, nil
	}
	return "", "", fmt.Errorf("the inventory %s names no address of %s; pass --address", c.inv.Path, name)
}

// certNames are what a node's certificate is for: its name, its hostname and its static
// addresses, as its config gives them.
func (c *cluster) certNames(name string) (pki.NodeNames, error) {
	if c.Partial() {
		return pki.NodeNames{}, errors.New("a node certificate names the node's addresses, which an inventory gives; pass --inventory")
	}
	nc, err := c.config(name)
	if err != nil {
		return pki.NodeNames{}, err
	}
	return sdk.NodeNames(name, nc.Documents), nil
}

// samePlatform refuses a node that runs an image of another platform than its config's image:
// its config and images would not fit the machine.
func (c *cluster) samePlatform(name, reported string) error {
	if c.Partial() {
		return nil
	}
	nc, md, err := c.image(name)
	if err != nil {
		return err
	}
	if reported == md.Platform {
		return nil
	}
	return fmt.Errorf("%s runs an image built for %s, but its image %s is built for %s; changing a node's platform is a reinstall", name, reported, nc.ImageName, md.Platform)
}

// errEncryptedSources tells that a node's config takes Secret values from sops or age sources,
// which a command that decrypts nothing cannot know.
var errEncryptedSources = errors.New("the config takes Secret values from sops or age sources")

// checkedConfig is the node's config as status and validate read it, which decrypt nothing: a key of
// a Secret from a sops or age source holds a placeholder, and encrypted names each such key.
func (c *cluster) checkedConfig(name string) (_ *inventory.NodeConfig, encrypted []string, _ error) {
	nc, err := c.config(name)
	if err != nil {
		return nil, nil, err
	}
	return c.sources.resolve(nc, true)
}

// version is the version of the config chalkctl delivers to a node, read without decrypting
// anything: errEncryptedSources when the config takes Secret values from sops or age sources.
func (c *cluster) version(name string) (string, error) {
	nc, encrypted, err := c.checkedConfig(name)
	if err != nil {
		return "", err
	}
	if len(encrypted) > 0 {
		return "", errEncryptedSources
	}
	e, err := config.Encode(nc.Documents)
	if err != nil {
		return "", err
	}
	return e.Version, nil
}
