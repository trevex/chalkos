package inventory

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/schema"
)

// NodeConfig is a node's config as the inventory gives it.
type NodeConfig struct {
	// Name is the inventory's name of the node, which its node certificate carries.
	Name string
	// Documents are the documents of the node's files, in their order, as the inventory's filters
	// leave them.
	Documents []config.Document
	// Hostname is the Host document's spec.hostname, empty without a Host or for a null one,
	// which take the node's name.
	Hostname string
	// ImageName is the name of the image the inventory gives the node; ImageDir is that image's
	// directory, absolute, or ImageOCI its reference in a registry, as its source names one of
	// them.
	ImageName, ImageDir, ImageOCI string
	// Image is the image's metadata.json, of its directory or its contract layer, once Resolve
	// read it. It stays on the operator's machine: the documents chalkctl delivers carry no root
	// hash.
	Image *schema.Metadata
	// inv is the inventory the config comes from, which reads an image in a registry once.
	inv *Inventory
}

// Node builds a node's config from its files and the inventory's image of it: their documents,
// of which no two share a kind and a name, as the inventory's filters leave them.
func (inv *Inventory) Node(name string) (*NodeConfig, error) {
	files, err := inv.Files(name)
	if err != nil {
		return nil, err
	}
	var docs []config.Document
	seen := map[string]string{}
	for _, f := range files {
		read, err := inv.read(f)
		if err != nil {
			return nil, fmt.Errorf("node %s: %w", name, err)
		}
		for _, d := range read {
			// The inventory reads a file once for every node that gets it; each node gets its own
			// copy, so that changing one node's documents changes no other's.
			d.Object = schema.DeepCopy(d.Object).(map[string]any)
			key := d.APIVersion + " " + d.Kind + " " + d.Name
			if other, ok := seen[key]; ok {
				return nil, fmt.Errorf("node %s: %s and %s both hold %s", name, inv.rel(other), inv.rel(f), d)
			}
			seen[key] = f
			docs = append(docs, d)
		}
	}
	imageName, source, err := inv.Image(name)
	if err != nil {
		return nil, err
	}
	c := &NodeConfig{Name: name, ImageName: imageName, ImageDir: source.Directory, ImageOCI: source.OCI, inv: inv}
	for _, d := range docs {
		if d.APIVersion == config.CoreAPI && d.Kind == "Host" {
			host, _ := d.Object["spec"].(map[string]any)
			c.Hostname, _ = host["hostname"].(string)
		}
	}
	for _, filter := range inv.filters {
		docs = filter(docs)
	}
	c.Documents = docs
	return c, nil
}

// StaticAddresses are the node's static addresses, as its Network documents give them.
func (c *NodeConfig) StaticAddresses() []string { return config.StaticAddresses(c.Documents) }

// Artifact is the node's image in a registry, read once per inventory.
func (c *NodeConfig) Artifact() (*ociimage.Artifact, error) {
	if c.ImageOCI == "" || c.inv == nil {
		return nil, fmt.Errorf("the image %s of node %s comes from no registry", c.ImageName, c.Name)
	}
	a, err := c.inv.Artifact(c.ImageOCI)
	if err != nil {
		return nil, fmt.Errorf("the image %s of node %s: %w", c.ImageName, c.Name, err)
	}
	return a, nil
}

// Resolve reads the metadata.json of the node's image once, from its directory or from the
// contract layer of its image in a registry: the image's platform, variant and root hash, which
// chalkctl checks images and nodes against. The node's documents stay as its files give them, so
// its config's version does not change with every build of its image.
func (c *NodeConfig) Resolve() (schema.Metadata, error) {
	if c.Image != nil {
		return *c.Image, nil
	}
	if c.ImageOCI != "" {
		a, err := c.Artifact()
		if err != nil {
			return schema.Metadata{}, err
		}
		// Read checked it against the image's header: its root hash and platform.
		md := a.Metadata
		c.Image = &md
		return md, nil
	}
	file := filepath.Join(c.ImageDir, "metadata.json")
	md, err := schema.ReadMetadata(c.ImageDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return schema.Metadata{}, fmt.Errorf("the image %s of node %s: %s has no metadata.json; build the image or link its directory there", c.ImageName, c.Name, c.ImageDir)
	case err != nil:
		return schema.Metadata{}, fmt.Errorf("the image %s of node %s: %w", c.ImageName, c.Name, err)
	}
	switch {
	case md.RootHash == "":
		return schema.Metadata{}, fmt.Errorf("the image %s of node %s: %s names no root hash", c.ImageName, c.Name, file)
	case !schema.IsRootHash(md.RootHash):
		return schema.Metadata{}, fmt.Errorf("the image %s of node %s: %s names the root hash %q, which is not 64 lower-case hex digits", c.ImageName, c.Name, file, md.RootHash)
	case md.Platform == "":
		return schema.Metadata{}, fmt.Errorf("the image %s of node %s: %s names no platform", c.ImageName, c.Name, file)
	}
	c.Image = &md
	return md, nil
}
