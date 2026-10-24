package chalkctl

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"slices"
	"strings"

	"github.com/spf13/pflag"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/inventory"
	"github.com/trevex/chalkos/pkg/pki"
)

// clusterFlags say where chalkctl learns the cluster from.
type clusterFlags struct{ inventory string }

func (c *clusterFlags) register(fs *pflag.FlagSet) {
	fs.StringVar(&c.inventory, "inventory", "", "inventory naming the cluster's nodes, their addresses and their config files (default "+inventory.DefaultFile+" in the current directory, when it exists)")
}

// cluster is what chalkctl knows of the cluster it operates: its name, the inventory's, which
// messages call it by, its Kubernetes endpoint, its nodes and their config, from an inventory.
// Without one it is partial and nameless: each command names its node and --address reaches it.
type cluster struct {
	name, endpoint string
	inv            *inventory.Inventory
	// configs holds each node's config, once read from the inventory.
	configs map[string]*inventory.NodeConfig
}

// partial reports whether the cluster comes without an inventory.
func (c *cluster) partial() bool { return c.inv == nil }

// loadCluster reads the inventory --inventory names, or else chalkos.yaml in the current
// directory, whose images in registries are read under ctx; without either, the cluster is
// partial.
func loadCluster(ctx context.Context, f clusterFlags) (*cluster, error) {
	if f.inventory != "" {
		return loadInventory(ctx, f.inventory)
	}
	switch _, err := os.Stat(inventory.DefaultFile); {
	case err == nil:
		return loadInventory(ctx, inventory.DefaultFile)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	return &cluster{}, nil
}

// loadInventory reads an inventory and the endpoint its nodes' Cluster documents name; every
// command refuses to act on an inventory whose nodes hold different Cluster or Flannel specs. The
// inventory reads its images in registries under ctx, the command's context.
func loadInventory(ctx context.Context, path string) (*cluster, error) {
	inv, err := inventory.Load(path)
	if err != nil {
		return nil, err
	}
	inv.SetContext(ctx)
	endpoint, err := inv.Endpoint()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cluster{name: inv.Name, endpoint: endpoint, inv: inv, configs: map[string]*inventory.NodeConfig{}}, nil
}

// requireNodes refuses a partial cluster for a command that needs the nodes' config.
func (c *cluster) requireNodes(command string) error {
	if c.partial() {
		return fmt.Errorf("%s needs an inventory, which gives the cluster's nodes and their config; pass --inventory", command)
	}
	return nil
}

// nodeNames are the inventory's nodes, sorted; none on a partial cluster.
func (c *cluster) nodeNames() []string {
	if c.partial() {
		return nil
	}
	return c.inv.NodeNames()
}

// node refuses a node the cluster does not have. A partial cluster has every node a command
// names.
func (c *cluster) node(name string) error {
	if c.partial() || slices.Contains(c.nodeNames(), name) {
		return nil
	}
	return fmt.Errorf("the cluster %s has no node %s; its nodes are %s", c.name, name, strings.Join(c.nodeNames(), ", "))
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

// kind is what a node is in Kubernetes: the type its Node document names; empty without
// Kubernetes and on a partial cluster.
func (c *cluster) kind(name string) (string, error) {
	if c.partial() {
		return "", nil
	}
	nc, err := c.config(name)
	if err != nil {
		return "", err
	}
	return nc.Type, nil
}

// nodesOf lists the cluster's nodes of a Kubernetes kind but skip, in name order; kind "" lists
// those without Kubernetes.
func (c *cluster) nodesOf(kind, skip string) ([]string, error) {
	var names []string
	for _, name := range c.nodeNames() {
		k, err := c.kind(name)
		if err != nil {
			return nil, err
		}
		if k == kind && name != skip {
			names = append(names, name)
		}
	}
	return names, nil
}

// platform is the platform of a node's image: that of the directory of its image in the
// inventory.
func (c *cluster) platform(name string) (string, error) {
	nc, err := c.config(name)
	if err != nil {
		return "", err
	}
	md, err := nc.Resolve()
	return md.Platform, err
}

// declares says where a node's platform comes from, for messages.
func (c *cluster) declares(name, platform string) string {
	nc, err := c.config(name)
	if err != nil {
		return "its config declares " + platform
	}
	return fmt.Sprintf("its image %s is built for %s", nc.ImageName, platform)
}

// inventoryImage is the config of the node in the inventory, which names the node's image: its
// directory or its reference in a registry.
func (c *cluster) inventoryImage(name string) (*inventory.NodeConfig, error) {
	if c.partial() {
		return nil, errors.New("the image comes from --image without an inventory, which names the nodes' images; pass --inventory")
	}
	return c.config(name)
}

// sameBuild refuses an image taken from the directory of a node's image whose store has another
// root hash than the directory's metadata.json names.
func (c *cluster) sameBuild(name string, img *diskImage) error {
	nc, err := c.config(name)
	if err != nil {
		return err
	}
	md, err := nc.Resolve()
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
	if c.partial() {
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
	if c.partial() {
		return pki.NodeNames{}, errors.New("a node certificate names the node's addresses, which an inventory gives; pass --inventory")
	}
	nc, err := c.config(name)
	if err != nil {
		return pki.NodeNames{}, err
	}
	hostnames := []string{name}
	if nc.Hostname != "" && nc.Hostname != name {
		hostnames = append(hostnames, nc.Hostname)
	}
	return pki.NodeNames{CommonName: name, DNSNames: hostnames, IPs: ipAddresses(nc.StaticAddresses())}, nil
}

// ipAddresses parses the addresses that are IPs, for the node certificate.
func ipAddresses(addrs []string) []net.IP {
	var ips []net.IP
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil {
			ips = append(ips, ip)
		}
	}
	return ips
}

// samePlatform refuses a node that runs an image of another platform than its config's image:
// its config and images would not fit the machine.
func (c *cluster) samePlatform(name, reported string) error {
	if c.partial() {
		return nil
	}
	want, err := c.platform(name)
	if err != nil {
		return err
	}
	if reported == want {
		return nil
	}
	return fmt.Errorf("%s runs an image built for %s, but %s; changing a node's platform is a reinstall", name, reported, c.declares(name, want))
}

// version is the version of the config chalkctl delivers to a node.
func (c *cluster) version(name string) (string, error) {
	nc, err := c.config(name)
	if err != nil {
		return "", err
	}
	e, err := config.Encode(nc.Documents)
	if err != nil {
		return "", err
	}
	return e.Version, nil
}
