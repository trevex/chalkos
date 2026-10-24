package inventory

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/ociimage"
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
		if c.Type != kubernetes.KindControlPlane && d.IsAddon() {
			continue
		}
		c.Documents = append(c.Documents, d)
	}
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

// clusterKinds are the kinds every node of the cluster holds alike: each node builds the cluster
// from its own documents, so nodes that differ would each run another one.
var clusterKinds = []string{"Cluster", "Flannel"}

// Endpoint checks that the nodes describe one cluster and returns the URL of its Kubernetes API
// server, the endpoint of their Cluster documents. Every node whose files hold a Cluster document
// holds the same Cluster spec, and the same Flannel spec or, on every one of them, none, whatever
// the documents are called; a node without a Cluster document runs no Kubernetes and is skipped.
// The specs compare as written, so a field left to its default differs from one set to it, and
// only the order of Flannel's vxlanSourceSubnets makes no difference. With no nodes, the
// endpoint is the one of the shared files' Cluster document, and it is empty when no node holds
// one.
func (inv *Inventory) Endpoint() (string, error) {
	// specs returns the spec of each cluster kind the files hold, nil for one they lack.
	specs := func(files []string) (map[string]map[string]any, error) {
		found := map[string][]map[string]any{}
		for _, f := range files {
			docs, err := inv.read(f)
			if err != nil {
				return nil, err
			}
			for _, d := range docs {
				if d.APIVersion == config.KubernetesAPI && slices.Contains(clusterKinds, d.Kind) {
					spec, _ := d.Object["spec"].(map[string]any)
					if spec == nil {
						spec = map[string]any{}
					}
					found[d.Kind] = append(found[d.Kind], spec)
				}
			}
		}
		out := map[string]map[string]any{}
		for _, kind := range clusterKinds {
			switch n := len(found[kind]); {
			case n == 1:
				out[kind] = found[kind][0]
			case n > 1:
				return nil, fmt.Errorf("its files hold %d %s documents, not one", n, kind)
			}
		}
		return out, nil
	}
	endpointOf := func(spec map[string]any) string {
		e, _ := spec["endpoint"].(string)
		return e
	}
	if len(inv.Spec.Nodes) == 0 {
		files, err := inv.expand(inv.Spec.All.Files)
		if err != nil {
			return "", err
		}
		s, err := specs(files)
		if err != nil {
			return "", fmt.Errorf("the shared files: %w", err)
		}
		return endpointOf(s["Cluster"]), nil
	}
	var nodes []string
	held := map[string]map[string]map[string]any{}
	for _, n := range inv.NodeNames() {
		files, err := inv.Files(n)
		if err != nil {
			return "", err
		}
		s, err := specs(files)
		if err != nil {
			return "", fmt.Errorf("node %s: %w", n, err)
		}
		if s["Cluster"] != nil {
			nodes = append(nodes, n)
			held[n] = s
		}
	}
	if len(nodes) == 0 {
		return "", nil
	}
	for _, kind := range clusterKinds {
		if err := alike(kind, nodes, func(n string) map[string]any { return held[n][kind] }); err != nil {
			return "", err
		}
	}
	return endpointOf(held[nodes[0]]["Cluster"]), nil
}

// alike refuses nodes whose specs of the kind, nil for none, differ. When some nodes hold none,
// the message names the nodes of each spec in the order of their first node; otherwise it names
// the first field whose values differ, walking the fields in name order, and the nodes of each of
// its values. Flannel's vxlanSourceSubnets compare as a set.
func alike(kind string, nodes []string, spec func(string) map[string]any) error {
	specs := make([]any, len(nodes))
	missing := false
	for i, n := range nodes {
		if s := spec(n); s != nil {
			specs[i] = normalized(kind, s)
		} else {
			missing = true
		}
	}
	var parts []string
	if missing {
		groups := groupNodes(nodes, specs)
		if len(groups) == 1 {
			return nil
		}
		seen := false
		for _, g := range groups {
			which := "another"
			switch {
			case g.value == nil:
				which = "none"
			case !seen:
				which, seen = "one", true
			}
			parts = append(parts, which+" on "+joinNames(g.nodes))
		}
	} else {
		path, ok := firstDifference(specs, nil)
		if !ok {
			return nil
		}
		values := make([]any, len(specs))
		for i, s := range specs {
			values[i] = at(s, path)
		}
		for i, g := range groupNodes(nodes, values) {
			part := show(g.value) + " on " + joinNames(g.nodes)
			if i == 0 {
				part = showPath(path) + " is " + part
			}
			parts = append(parts, part)
		}
	}
	return fmt.Errorf("the nodes hold different %s specs: %s; every node of the cluster holds the same Cluster and Flannel specs", kind, strings.Join(parts, ", "))
}

// normalized is a spec of the kind as alike compares it: Flannel's vxlanSourceSubnets sorted, as
// their order makes no difference to the ranges flannel takes VXLAN from.
func normalized(kind string, spec map[string]any) map[string]any {
	subnets, ok := spec["vxlanSourceSubnets"].([]any)
	if kind != "Flannel" || !ok {
		return spec
	}
	sorted := slices.Clone(subnets)
	slices.SortStableFunc(sorted, func(a, b any) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) })
	out := maps.Clone(spec)
	out["vxlanSourceSubnets"] = sorted
	return out
}

// unset stands for a field a spec lacks.
type unset struct{}

// firstDifference returns the path, below path, of the first field whose values differ: a
// field's name or a list item's index for each step. It walks the fields of objects in name order
// and the items of lists of one length in their order, and returns false when the values are
// equal.
func firstDifference(values []any, path []any) ([]any, bool) {
	if !slices.ContainsFunc(values, func(v any) bool { return !reflect.DeepEqual(v, values[0]) }) {
		return nil, false
	}
	if objects, ok := every[map[string]any](values); ok {
		var keys []string
		for _, o := range objects {
			keys = append(keys, slices.Collect(maps.Keys(o))...)
		}
		slices.Sort(keys)
		for _, k := range slices.Compact(keys) {
			field := make([]any, len(objects))
			for i, o := range objects {
				field[i] = at(o, []any{k})
			}
			if p, ok := firstDifference(field, append(slices.Clip(path), k)); ok {
				return p, true
			}
		}
	}
	lists, ok := every[[]any](values)
	if ok && !slices.ContainsFunc(lists, func(l []any) bool { return len(l) != len(lists[0]) }) {
		for i := range lists[0] {
			item := make([]any, len(lists))
			for j, l := range lists {
				item[j] = l[i]
			}
			if p, ok := firstDifference(item, append(slices.Clip(path), i)); ok {
				return p, true
			}
		}
	}
	return path, true
}

// every returns values as Ts when each is one.
func every[T any](values []any) ([]T, bool) {
	out := make([]T, len(values))
	for i, v := range values {
		t, ok := v.(T)
		if !ok {
			return nil, false
		}
		out[i] = t
	}
	return out, true
}

// at returns the value at path below v, unset when v lacks it.
func at(v any, path []any) any {
	for _, step := range path {
		switch s := step.(type) {
		case string:
			o, ok := v.(map[string]any)
			if !ok {
				return unset{}
			}
			if v, ok = o[s]; !ok {
				return unset{}
			}
		case int:
			l, ok := v.([]any)
			if !ok || s >= len(l) {
				return unset{}
			}
			v = l[s]
		}
	}
	return v
}

// showPath writes a path below a document's spec as spec.vip.addresses[0].
func showPath(path []any) string {
	var b strings.Builder
	b.WriteString("spec")
	for _, step := range path {
		if i, ok := step.(int); ok {
			fmt.Fprintf(&b, "[%d]", i)
		} else {
			fmt.Fprintf(&b, ".%s", step)
		}
	}
	return b.String()
}

// valueGroup is the nodes of one value, in their order.
type valueGroup struct {
	value any
	nodes []string
}

// groupNodes groups the nodes by their values, the group of the first node first.
func groupNodes(nodes []string, values []any) []*valueGroup {
	var groups []*valueGroup
	for i, n := range nodes {
		j := slices.IndexFunc(groups, func(g *valueGroup) bool { return reflect.DeepEqual(g.value, values[i]) })
		if j < 0 {
			groups = append(groups, &valueGroup{value: values[i]})
			j = len(groups) - 1
		}
		groups[j].nodes = append(groups[j].nodes, n)
	}
	return groups
}

// show writes a value as JSON, and a field a spec lacks as unset.
func show(v any) string {
	if _, ok := v.(unset); ok {
		return "unset"
	}
	var b strings.Builder
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// joinNames lists names as prose: a, b and c.
func joinNames(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
