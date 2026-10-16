package chalkctl

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"os"
	"slices"
	"strings"

	"github.com/spf13/pflag"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/inventory"
	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/pki"
)

// clusterFlags say where chalkctl learns the cluster from.
type clusterFlags struct {
	inventory, manifest string
}

func (c *clusterFlags) register(fs *pflag.FlagSet) {
	fs.StringVar(&c.inventory, "inventory", "", "inventory naming the cluster's nodes, their addresses and their config files (default "+inventory.DefaultFile+" in the current directory, when it exists)")
	fs.StringVar(&c.manifest, "manifest", "", "read the cluster's nodes from this cluster definition's manifest instead of an inventory")
}

// cluster is what chalkctl knows of the cluster it operates: its name and endpoint, its nodes and
// their config, from an inventory or from a cluster definition's manifest. Without either it is
// partial: a client file names it, each command names its node, and --address or the client file
// gives the node's address.
type cluster struct {
	name, endpoint string
	// signerCertificate is the PEM certificate of the Secure Boot db key the nodes' images are
	// signed with, as a manifest names it; empty for an inventory, which leaves it to --sign-cert.
	signerCertificate string
	partial           bool
	// clientNodes are the nodes a client file names, on a partial cluster.
	clientNodes []string
	inv         *inventory.Inventory
	manifest    *manifest.Manifest
	// configs holds each node's config, once read from the inventory.
	configs map[string]*inventory.NodeConfig
}

// loadCluster reads the inventory --inventory names, the manifest --manifest names, or else
// chalkos.yaml in the current directory; without any, the cluster is partial.
func loadCluster(f clusterFlags) (*cluster, error) {
	switch {
	case f.inventory != "" && f.manifest != "":
		return nil, errors.New("pass --inventory or --manifest, not both")
	case f.manifest != "":
		return loadManifest(f.manifest)
	case f.inventory != "":
		return loadInventory(f.inventory)
	}
	switch _, err := os.Stat(inventory.DefaultFile); {
	case err == nil:
		return loadInventory(inventory.DefaultFile)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	return &cluster{partial: true}, nil
}

// loadInventory reads an inventory and the cluster its nodes' Cluster documents name.
func loadInventory(path string) (*cluster, error) {
	inv, err := inventory.Load(path)
	if err != nil {
		return nil, err
	}
	name, endpoint, err := inv.Cluster()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cluster{name: name, endpoint: endpoint, inv: inv, configs: map[string]*inventory.NodeConfig{}}, nil
}

// loadManifest reads a cluster definition's manifest from a file.
func loadManifest(path string) (*cluster, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m, err := manifest.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return clusterOfManifest(m), nil
}

// clusterOfManifest is the cluster a manifest describes.
func clusterOfManifest(m *manifest.Manifest) *cluster {
	return &cluster{name: m.Cluster.Name, endpoint: m.Cluster.Endpoint, signerCertificate: m.SecureBoot.SignerCertificate, manifest: m}
}

// checkCredentials names a partial cluster, and its nodes, after the client file, and refuses a
// client file of another cluster.
func (c *cluster) checkCredentials(creds *credentials) error {
	if creds.config == nil {
		return nil
	}
	if c.partial {
		c.name = creds.config.Cluster
		c.clientNodes = slices.Sorted(maps.Keys(creds.config.Nodes))
		return nil
	}
	if creds.config.Cluster != c.name {
		return fmt.Errorf("%s is a client file of the cluster %s, not %s", creds.configPath, creds.config.Cluster, c.name)
	}
	return nil
}

// source is where the cluster comes from, as messages name it.
func (c *cluster) source() string {
	if c.manifest != nil {
		return "the cluster definition"
	}
	return "the inventory"
}

// requireNodes refuses a partial cluster for a command that needs the nodes' config.
func (c *cluster) requireNodes(command string) error {
	if c.partial {
		return fmt.Errorf("%s needs an inventory, which gives the cluster's nodes and their config; pass --inventory", command)
	}
	return nil
}

// nodeNames are the cluster's nodes, sorted.
func (c *cluster) nodeNames() []string {
	switch {
	case c.inv != nil:
		return c.inv.NodeNames()
	case c.manifest != nil:
		return slices.Sorted(maps.Keys(c.manifest.Nodes))
	}
	return c.clientNodes
}

// node refuses a node the cluster does not have. A partial cluster has every node a command
// names.
func (c *cluster) node(name string) error {
	if c.partial || slices.Contains(c.nodeNames(), name) {
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

// kind is what a node is in Kubernetes: the type its Node document names, or the kind the
// cluster definition gives it; empty without Kubernetes and on a partial cluster.
func (c *cluster) kind(name string) (string, error) {
	switch {
	case c.manifest != nil:
		return c.manifest.Nodes[name].Kind, nil
	case c.inv != nil:
		nc, err := c.config(name)
		if err != nil {
			return "", err
		}
		return nc.Type, nil
	}
	return "", nil
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

// platform is the platform of a node's image: that of its Image's directory, or the one the
// cluster definition declares.
func (c *cluster) platform(name string) (string, error) {
	if c.manifest != nil {
		return c.manifest.Nodes[name].Platform, nil
	}
	nc, err := c.config(name)
	if err != nil {
		return "", err
	}
	md, err := nc.Resolve()
	return md.Platform, err
}

// declares says where a node's platform comes from, for messages.
func (c *cluster) declares(name, platform string) string {
	if c.manifest != nil {
		return "the cluster definition declares it on " + platform
	}
	nc, err := c.config(name)
	if err != nil {
		return "its config declares " + platform
	}
	return fmt.Sprintf("its Image %s is built for %s", nc.ImageName, platform)
}

// imageDir is the directory of the image a node's Image names. A cluster definition names its
// images by flake attributes, which chalkctl does not build.
func (c *cluster) imageDir(name string) (string, error) {
	if c.inv == nil {
		return "", errors.New("the image comes from --image without an inventory, whose Image documents name the nodes' image directories")
	}
	nc, err := c.config(name)
	if err != nil {
		return "", err
	}
	return nc.ImageDir, nil
}

// sameBuild refuses an image taken from a node's Image directory whose store has another root hash
// than the directory's metadata.json names, which the node stores in its Image.
func (c *cluster) sameBuild(name string, img *diskImage) error {
	nc, err := c.config(name)
	if err != nil {
		return err
	}
	md, err := nc.Resolve()
	if err != nil {
		return err
	}
	if got := hex.EncodeToString(img.store.RootHash); got != md.RootHash {
		return fmt.Errorf("%s: metadata.json names the root hash %s, but the image's store has %s; rebuild the image or link its directory again", nc.ImageDir, md.RootHash, got)
	}
	return nil
}

// address returns where a node's chalkd is reached and where that address comes from: --address;
// else the inventory's address of the node; else, from a cluster definition, the address a client
// file names when it prefers its addresses, as chalklab's does, or the node's first static
// address; else, on a partial cluster, the address the client file names.
func (c *cluster) address(flagValue string, creds *credentials, name string) (addr, source string, err error) {
	if flagValue != "" {
		return flagValue, "--address", nil
	}
	if c.inv != nil {
		if a := c.inv.Spec.Nodes[name].Address; a != "" {
			return a, "the inventory " + c.inv.Path, nil
		}
		return "", "", fmt.Errorf("the inventory %s names no address of %s; pass --address", c.inv.Path, name)
	}
	if creds != nil && creds.config != nil && (c.partial || creds.config.PreferNodeAddresses) {
		if a := creds.config.Nodes[name]; a != "" {
			return a, "the client file " + creds.configPath, nil
		}
	}
	if c.partial {
		return "", "", fmt.Errorf("no address of %s is known; pass --address", name)
	}
	addrs := c.staticAddresses(name)
	if len(addrs) == 0 {
		return "", "", fmt.Errorf("node %s has no static address; pass --address", name)
	}
	return addrs[0], "the cluster definition", nil
}

// staticAddresses returns a node's static addresses from a cluster definition: those of the
// documents chalkctl delivers to it, or, when it cannot generate them, those of the identity the
// definition renders, so the node stays reachable, to status among others, which reports why.
func (c *cluster) staticAddresses(name string) []string {
	if b, err := config.FromManifest(c.manifest, name); err == nil {
		return config.StaticAddresses(b.Documents)
	}
	return c.manifest.Nodes[name].Identity.StaticAddresses()
}

// certNames are what a node's certificate is for: its name, its hostname and its static
// addresses, as its config gives them.
func (c *cluster) certNames(name string) (pki.NodeNames, error) {
	var hostname string
	var addrs []string
	switch {
	case c.manifest != nil:
		id := c.manifest.Nodes[name].Identity
		hostname, addrs = id.Hostname, id.StaticAddresses()
	case c.inv != nil:
		nc, err := c.config(name)
		if err != nil {
			return pki.NodeNames{}, err
		}
		hostname, addrs = nc.Hostname, nc.StaticAddresses()
	default:
		return pki.NodeNames{}, errors.New("a node certificate names the node's addresses, which an inventory gives; pass --inventory")
	}
	hostnames := []string{name}
	if hostname != "" && hostname != name {
		hostnames = append(hostnames, hostname)
	}
	return pki.NodeNames{CommonName: name, DNSNames: hostnames, IPs: ipAddresses(addrs)}, nil
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
	if c.partial {
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

// platformLine is the status line of the platform the node runs on, compared with its config's.
func (c *cluster) platformLine(name, reported string) string {
	if c.partial {
		return "platform " + reported
	}
	want, err := c.platform(name)
	switch {
	case err != nil:
		return fmt.Sprintf("platform %s; the platform of its config's image is unknown: %v", reported, err)
	case reported != want:
		return fmt.Sprintf("platform %s, but %s; changing a node's platform is a reinstall", reported, c.declares(name, want))
	}
	return fmt.Sprintf("platform %s (%s's)", reported, c.source())
}

// version is the version of the config chalkctl delivers to a node.
func (c *cluster) version(name string) (string, error) {
	if c.manifest != nil {
		b, err := config.FromManifest(c.manifest, name)
		if err != nil {
			return "", err
		}
		return b.Version, nil
	}
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

// configState says whether a node runs the config chalkctl delivers to it.
func (c *cluster) configState(name, running string) string {
	if c.partial {
		return "no inventory to compare it with"
	}
	want, err := c.version(name)
	switch {
	case err != nil:
		return "cannot read the config " + c.source() + " gives it: " + err.Error()
	case want != running:
		return "not " + c.source() + "'s; chalkctl apply-config " + name + " delivers it"
	}
	return c.source() + "'s"
}

// imageLine is the status line of a node whose store has another root hash than the image its
// config's Image names, or whose Image's directory cannot be read; empty when it has that image,
// or when the node or its config is unknown, which the config line reports.
func (c *cluster) imageLine(name string, boot *nodev1.BootStatus) string {
	if c.inv == nil || len(boot.GetRootHash()) == 0 {
		return ""
	}
	nc, err := c.config(name)
	if err != nil {
		return ""
	}
	md, err := nc.Resolve()
	switch {
	case err != nil:
		return fmt.Sprintf("its store is not compared with the image of its config: %v", err)
	case hex.EncodeToString(boot.GetRootHash()) == md.RootHash:
		return ""
	}
	line := fmt.Sprintf("its store's root hash is %x, not that of the Image %s its config names, %s", boot.GetRootHash(), nc.ImageName, md.RootHash)
	// After an upgrade with --no-reboot the image waits for the node's next boot.
	if want, err := hex.DecodeString(md.RootHash); err == nil && hasStaged(boot, md.Version, want) {
		return fmt.Sprintf("%s; it has %s staged, which it boots with its next reboot: chalkctl reboot %s", line, md.Version, name)
	}
	return fmt.Sprintf("%s; chalkctl upgrade --nodes %s installs that image", line, name)
}
