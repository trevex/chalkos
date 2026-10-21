package inventory

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/schema"
)

// NodeConfig is a node's config as the inventory gives it.
type NodeConfig struct {
	// Name is the inventory's name of the node, which its node certificate carries.
	Name string
	// Documents are the documents of the node's files, in their order. A node that is no control
	// plane gets no Addon document: an Addon's objects go to the cluster's API, which only control
	// planes apply.
	Documents []config.Document
	// Type is the Kubernetes Node document's spec.type, empty without one, as on an image without
	// Kubernetes, and Hostname the Host document's spec.hostname, empty without a Host or for a
	// null one, which take the node's name.
	Type, Hostname string
	// ImageName is the name of the image the inventory gives the node, and ImageDir that image's
	// directory, absolute.
	ImageName, ImageDir string
	// Image is ImageDir's metadata.json, once Resolve read it. It stays on the operator's machine:
	// the documents chalkctl delivers carry no root hash.
	Image *schema.Metadata
}

// Node builds a node's config from its files and the inventory's image of it: their documents,
// of which no two share a kind and a name.
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
			d.Object = cloneValue(d.Object).(map[string]any)
			key := d.APIVersion + " " + d.Kind + " " + d.Name
			if other, ok := seen[key]; ok {
				return nil, fmt.Errorf("node %s: %s and %s both hold %s", name, inv.rel(other), inv.rel(f), d)
			}
			seen[key] = f
			docs = append(docs, d)
		}
	}
	imageName, imageDir, err := inv.Image(name)
	if err != nil {
		return nil, err
	}
	c := &NodeConfig{Name: name, ImageName: imageName, ImageDir: imageDir}
	for _, d := range docs {
		if d.APIVersion == config.KubernetesAPI && d.Kind == "Node" {
			node, _ := d.Object["spec"].(map[string]any)
			c.Type, _ = node["type"].(string)
		}
		if d.APIVersion == config.CoreAPI && d.Kind == "Host" {
			host, _ := d.Object["spec"].(map[string]any)
			c.Hostname, _ = host["hostname"].(string)
		}
	}
	for _, d := range docs {
		if c.Type != identity.KindControlPlane && d.APIVersion == config.KubernetesAPI && d.Kind == "Addon" {
			continue
		}
		c.Documents = append(c.Documents, d)
	}
	return c, nil
}

// cloneValue copies a document's value, its maps and lists included.
func cloneValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = cloneValue(e)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = cloneValue(e)
		}
		return out
	}
	return v
}

// rootHash is a store's dm-verity root hash as metadata.json writes it.
var rootHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

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
	case !rootHash.MatchString(md.RootHash):
		return schema.Metadata{}, fmt.Errorf("the image %s of node %s: %s names the root hash %q, which is not 64 lower-case hex digits", c.ImageName, c.Name, file, md.RootHash)
	case md.Platform == "":
		return schema.Metadata{}, fmt.Errorf("the image %s of node %s: %s names no platform", c.ImageName, c.Name, file)
	}
	c.Image = &md
	return md, nil
}

// Endpoint returns the URL of the cluster's Kubernetes API server: the endpoint the Cluster
// documents of the nodes' files name, alike, or with no nodes the one of the shared files'
// Cluster document. A node whose files hold no Cluster document names none and is skipped; the
// endpoint is empty when no node names one.
func (inv *Inventory) Endpoint() (string, error) {
	// of is the endpoint of the Cluster document the files hold, and whether they hold one.
	of := func(files []string) (string, bool, error) {
		var found []string
		for _, f := range files {
			docs, err := inv.read(f)
			if err != nil {
				return "", false, err
			}
			for _, d := range docs {
				if d.APIVersion == config.KubernetesAPI && d.Kind == "Cluster" {
					spec, _ := d.Object["spec"].(map[string]any)
					e, _ := spec["endpoint"].(string)
					found = append(found, e)
				}
			}
		}
		switch len(found) {
		case 0:
			return "", false, nil
		case 1:
			return found[0], true, nil
		}
		return "", false, fmt.Errorf("its files hold %d Cluster documents, not one", len(found))
	}
	if len(inv.Spec.Nodes) == 0 {
		files, err := inv.expand(inv.Spec.Files)
		if err != nil {
			return "", err
		}
		e, _, err := of(files)
		if err != nil {
			return "", fmt.Errorf("the shared files: %w", err)
		}
		return e, nil
	}
	var first, firstNode string
	for _, n := range inv.NodeNames() {
		files, err := inv.Files(n)
		if err != nil {
			return "", err
		}
		e, ok, err := of(files)
		if err != nil {
			return "", fmt.Errorf("node %s: %w", n, err)
		}
		switch {
		case !ok:
		case firstNode == "":
			first, firstNode = e, n
		case e != first:
			return "", fmt.Errorf("the nodes %s and %s name different endpoints in their Cluster documents: %q and %q", firstNode, n, first, e)
		}
	}
	return first, nil
}
