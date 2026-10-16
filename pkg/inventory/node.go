package inventory

import (
	"fmt"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/schema"
)

// NodeConfig is a node's config as the inventory gives it.
type NodeConfig struct {
	// Name is the node's name, which its Node document carries too.
	Name string
	// Documents are the documents of the node's files, in their order. A node that is no control
	// plane gets no Addon document: an Addon's objects go to the cluster's API, which only control
	// planes apply.
	Documents []config.Document
	// Type is the Node document's spec.type, empty without one, and Hostname its spec.hostname,
	// empty for the node's name.
	Type, Hostname string
	// ImageName is the name of the Image document the Node names, and ImageDir the directory its
	// source names, resolved against the file that holds the Image.
	ImageName, ImageDir string
	// Image is ImageDir's metadata.json, once Resolve read it. It stays on the operator's machine:
	// the documents chalkctl delivers carry no root hash.
	Image *schema.Metadata
}

// Node builds a node's config from its files: their documents, of which no two share a kind and
// a name, with exactly one Node, named as the inventory names the node, and the Image it names.
func (inv *Inventory) Node(name string) (*NodeConfig, error) {
	files, err := inv.Files(name)
	if err != nil {
		return nil, err
	}
	var docs []config.Document
	var sources []string
	seen := map[string]string{}
	for _, f := range files {
		read, err := inv.read(f)
		if err != nil {
			return nil, err
		}
		for _, d := range read {
			key := d.APIVersion + " " + d.Kind + " " + d.Name
			if other, ok := seen[key]; ok {
				return nil, fmt.Errorf("node %s: %s and %s both hold %s", name, inv.rel(other), inv.rel(f), d)
			}
			seen[key] = f
			docs = append(docs, d)
			sources = append(sources, f)
		}
	}
	c := &NodeConfig{Name: name}
	var node *config.Document
	for i := range docs {
		if docs[i].APIVersion != config.CoreAPI || docs[i].Kind != "Node" {
			continue
		}
		if node != nil {
			return nil, fmt.Errorf("node %s: its files hold the Nodes %q and %q", name, node.Name, docs[i].Name)
		}
		node = &docs[i]
	}
	if node == nil {
		return nil, fmt.Errorf("node %s: its files hold no Node document", name)
	}
	if node.Name != name {
		return nil, fmt.Errorf("node %s: its files hold the Node %q; a node's Node document carries the name the inventory gives it", name, node.Name)
	}
	spec, _ := node.Object["spec"].(map[string]any)
	c.Type, _ = spec["type"].(string)
	c.Hostname, _ = spec["hostname"].(string)
	ref, _ := spec["imageRef"].(string)
	for i, d := range docs {
		if c.Type != identity.KindControlPlane && d.APIVersion == config.KubernetesAPI && d.Kind == "Addon" {
			continue
		}
		if d.APIVersion == config.CoreAPI && d.Kind == "Image" && d.Name == ref {
			dir, err := sourceDirectory(d)
			if err != nil {
				return nil, fmt.Errorf("node %s: %w", name, err)
			}
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(filepath.Dir(sources[i]), dir)
			}
			c.ImageName, c.ImageDir = ref, filepath.Clean(dir)
		}
		c.Documents = append(c.Documents, d)
	}
	if c.ImageName == "" {
		return nil, fmt.Errorf("node %s runs the Image %q, which its files do not hold", name, ref)
	}
	return c, nil
}

// sourceDirectory is the directory an Image document's source names.
func sourceDirectory(d config.Document) (string, error) {
	spec, _ := d.Object["spec"].(map[string]any)
	source, _ := spec["source"].(map[string]any)
	dir, _ := source["directory"].(string)
	if dir == "" {
		return "", fmt.Errorf("its Image %q names no source directory", d.Name)
	}
	return dir, nil
}

// StaticAddresses are the node's static addresses, as its Network documents give them.
func (c *NodeConfig) StaticAddresses() []string { return config.StaticAddresses(c.Documents) }

// Resolve reads the metadata.json of the node's image directory once: the image's platform,
// variant and root hash, which chalkctl checks images and nodes against. The node's documents
// stay as its files give them, so its config's version does not change with every build of its
// image.
func (c *NodeConfig) Resolve() (schema.Metadata, error) {
	if c.Image != nil {
		return *c.Image, nil
	}
	md, err := schema.ReadMetadata(c.ImageDir)
	if err != nil {
		return schema.Metadata{}, fmt.Errorf("the Image %q of node %s: %w", c.ImageName, c.Name, err)
	}
	if md.RootHash == "" {
		return schema.Metadata{}, fmt.Errorf("the Image %q of node %s: %s names no root hash", c.ImageName, c.Name, filepath.Join(c.ImageDir, "metadata.json"))
	}
	c.Image = &md
	return md, nil
}

// Cluster returns the name and endpoint of the cluster: those of the Cluster document every
// node's files hold, alike, or with no nodes those of the shared files. The endpoint is empty
// when the Cluster names none.
func (inv *Inventory) Cluster() (name, endpoint string, err error) {
	type cluster struct{ name, endpoint string }
	of := func(files []string) (cluster, error) {
		var found []cluster
		for _, f := range files {
			docs, err := inv.read(f)
			if err != nil {
				return cluster{}, err
			}
			for _, d := range docs {
				if d.APIVersion == config.CoreAPI && d.Kind == "Cluster" {
					spec, _ := d.Object["spec"].(map[string]any)
					e, _ := spec["endpoint"].(string)
					found = append(found, cluster{d.Name, e})
				}
			}
		}
		if len(found) != 1 {
			return cluster{}, fmt.Errorf("its files hold %d Cluster documents, not one", len(found))
		}
		return found[0], nil
	}
	if len(inv.Spec.Nodes) == 0 {
		files, err := inv.expand(inv.Spec.Files)
		if err != nil {
			return "", "", err
		}
		c, err := of(files)
		if err != nil {
			return "", "", fmt.Errorf("the shared files: %w", err)
		}
		return c.name, c.endpoint, nil
	}
	var first cluster
	var firstNode string
	for _, n := range inv.NodeNames() {
		files, err := inv.Files(n)
		if err != nil {
			return "", "", err
		}
		c, err := of(files)
		if err != nil {
			return "", "", fmt.Errorf("node %s: %w", n, err)
		}
		if firstNode == "" {
			first, firstNode = c, n
			continue
		}
		if c != first {
			return "", "", fmt.Errorf("the nodes %s and %s are in different clusters: %s at %q and %s at %q", firstNode, n, first.name, first.endpoint, c.name, c.endpoint)
		}
	}
	return first.name, first.endpoint, nil
}
