// Package inventory reads chalkctl's inventory: the file that names a cluster's nodes, the
// address chalkctl reaches each at, the image each runs and the config files each node gets.
// Paths in an inventory are relative to its directory, and its file lists allow globs.
package inventory

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

const (
	// APIVersion and Kind identify an inventory.
	APIVersion = "chalkctl.chalkos.dev/v1alpha1"
	Kind       = "Inventory"
	// DefaultFile is the inventory chalkctl reads in the current directory when --inventory
	// names none.
	DefaultFile = "chalkos.yaml"
	// schemaFile is the name of the inventory's JSON Schema, as an image names its kinds'.
	schemaFile = "chalkctl.chalkos.dev_v1alpha1_Inventory.json"
)

//go:embed chalkctl.chalkos.dev_v1alpha1_Inventory.json
var schemaJSON []byte

// Document is an inventory file as it is written.
type Document struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       Spec     `json:"spec"`
}

// Metadata names the inventory.
type Metadata struct {
	Name string `json:"name"`
}

// Spec is what an inventory says of its cluster's nodes. It names no secrets file and no
// Secure Boot certificate: an inventory may be shared with whoever operates the nodes, and
// chalkctl takes those from its flags.
type Spec struct {
	// Images are the images the nodes run, by name.
	Images map[string]Image `json:"images,omitempty"`
	// Image is the image of the nodes that name none and whose groups name none.
	Image string `json:"image,omitempty"`
	// Files are the config files every node gets, as paths or globs.
	Files  []string         `json:"files,omitempty"`
	Groups map[string]Group `json:"groups,omitempty"`
	Nodes  map[string]Node  `json:"nodes,omitempty"`
}

// Image is an image the nodes may run.
type Image struct {
	Source ImageSource `json:"source"`
}

// ImageSource is where an image comes from: the directory of an image build, as nix build makes
// it.
type ImageSource struct {
	Directory string `json:"directory"`
}

// Group is the image and the config files that the nodes in the group get.
type Group struct {
	Image string   `json:"image,omitempty"`
	Files []string `json:"files,omitempty"`
}

// Node is a node of the cluster: where chalkctl reaches it, the image it runs, the groups it is
// in and its own config files.
type Node struct {
	Address string   `json:"address,omitempty"`
	Image   string   `json:"image,omitempty"`
	Groups  []string `json:"groups,omitempty"`
	Files   []string `json:"files,omitempty"`
}

// Inventory is an inventory as chalkctl reads it: its spec with every path absolute. Its files
// are globs, in which the inventory's directory is escaped; its image directories are plain
// paths.
type Inventory struct {
	// Path is the inventory file's absolute path.
	Path string
	Name string
	Spec Spec
	// parsed holds the documents of each config file read so far, by path.
	parsed map[string][]config.Document
}

// Load reads an inventory and checks it against its schema and its own references: every group a
// node is in and every image named must be defined, and every node must have one image.
func Load(path string) (*Inventory, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	inv, err := parse(abs, data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return inv, nil
}

// parse reads the inventory at path from data: one document, valid against the inventory's
// schema, with its paths made absolute against its directory and an image for every node.
func parse(path string, data []byte) (*Inventory, error) {
	docs, err := config.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if len(docs) != 1 {
		return nil, fmt.Errorf("holds %d documents; an inventory is one", len(docs))
	}
	s, err := schema.LoadFiles(map[string][]byte{schemaFile: schemaJSON})
	if err != nil {
		return nil, err
	}
	if err := s.Validate(docs[0].Object); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(docs[0].Object)
	if err != nil {
		return nil, err
	}
	var d Document
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	spec := d.Spec
	images := map[string]Image{}
	for name, im := range spec.Images {
		images[name] = Image{Source: ImageSource{Directory: absolute(dir, im.Source.Directory)}}
	}
	defined := func(image, where string) error {
		if _, ok := images[image]; image != "" && !ok {
			return fmt.Errorf("%s names the image %s, which the inventory does not define", where, image)
		}
		return nil
	}
	if err := defined(spec.Image, "spec.image"); err != nil {
		return nil, err
	}
	spec.Files = absoluteAll(dir, spec.Files)
	groups := map[string]Group{}
	for _, name := range slices.Sorted(maps.Keys(spec.Groups)) {
		g := spec.Groups[name]
		if err := defined(g.Image, "the group "+name); err != nil {
			return nil, err
		}
		groups[name] = Group{Image: g.Image, Files: absoluteAll(dir, g.Files)}
	}
	nodes := map[string]Node{}
	for _, name := range slices.Sorted(maps.Keys(spec.Nodes)) {
		n := spec.Nodes[name]
		for _, g := range n.Groups {
			if _, ok := groups[g]; !ok {
				return nil, fmt.Errorf("node %s is in the group %s, which the inventory does not define", name, g)
			}
		}
		if err := defined(n.Image, "node "+name); err != nil {
			return nil, err
		}
		n.Files = absoluteAll(dir, n.Files)
		nodes[name] = n
	}
	spec.Images, spec.Groups, spec.Nodes = images, groups, nodes
	inv := &Inventory{Path: path, Name: d.Metadata.Name, Spec: spec, parsed: map[string][]config.Document{}}
	for _, name := range inv.NodeNames() {
		if _, _, err := inv.Image(name); err != nil {
			return nil, err
		}
	}
	return inv, nil
}

// Image is the name and directory of the image a node runs: its own, else that of its groups,
// else spec.image. Groups that name different images are refused even for a node that names its
// own, so that the conflict shows before the node leaves the group or drops its image.
func (inv *Inventory) Image(node string) (name, dir string, err error) {
	n, ok := inv.Spec.Nodes[node]
	if !ok {
		return "", "", inv.noNode(node)
	}
	var group string
	for _, g := range n.Groups {
		image := inv.Spec.Groups[g].Image
		switch {
		case image == "" || image == name:
		case name != "":
			return "", "", fmt.Errorf("node %s is in the groups %s and %s, which name different images: %s and %s", node, group, g, name, image)
		default:
			name, group = image, g
		}
	}
	if n.Image != "" {
		name = n.Image
	}
	if name == "" {
		name = inv.Spec.Image
	}
	if name == "" {
		return "", "", fmt.Errorf("node %s has no image; name one in spec.image, in a group of the node or on the node", node)
	}
	return name, inv.Spec.Images[name].Source.Directory, nil
}

// absolute resolves a path of the inventory against its directory; empty stays empty.
func absolute(dir, p string) string {
	switch {
	case p == "":
		return ""
	case filepath.IsAbs(p):
		return filepath.Clean(p)
	}
	return filepath.Join(dir, p)
}

// absoluteAll resolves paths and globs of the inventory against its directory. The directory
// joins them escaped, so that a directory such as lab[1] stays a literal part of each glob; a
// glob written absolute stays as written.
func absoluteAll(dir string, patterns []string) []string {
	var out []string
	for _, p := range patterns {
		out = append(out, absolute(escapeGlob(dir), p))
	}
	return out
}

// escapeGlob escapes the characters filepath.Match reads as a pattern's.
func escapeGlob(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`*?[\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Dir is the inventory's directory.
func (inv *Inventory) Dir() string { return filepath.Dir(inv.Path) }

// NodeNames are the inventory's nodes, sorted.
func (inv *Inventory) NodeNames() []string { return slices.Sorted(maps.Keys(inv.Spec.Nodes)) }

// Files are the config files of a node, in the order their documents form its set: the shared
// files, then its groups' files in the order it names the groups, then its own. Each path or glob
// must name at least one file; a glob's files come in name order, and a file named twice counts
// where it is named first. Its errors name the node.
func (inv *Inventory) Files(node string) ([]string, error) {
	n, ok := inv.Spec.Nodes[node]
	if !ok {
		return nil, inv.noNode(node)
	}
	patterns := slices.Clone(inv.Spec.Files)
	for _, g := range n.Groups {
		patterns = append(patterns, inv.Spec.Groups[g].Files...)
	}
	patterns = append(patterns, n.Files...)
	files, err := inv.expand(patterns)
	if err != nil {
		return nil, fmt.Errorf("node %s: %w", node, err)
	}
	return files, nil
}

// noNode is the error for a node the inventory does not have.
func (inv *Inventory) noNode(node string) error {
	return fmt.Errorf("the inventory %s has no node %s; its nodes are %s", inv.Path, node, strings.Join(inv.NodeNames(), ", "))
}

// expand lists the files of paths and globs, each of which must name one at least, once each in
// the order they are first named. A glob skips the directories it matches; a path that names a
// directory alone is refused.
func (inv *Inventory) expand(patterns []string) ([]string, error) {
	var files []string
	seen := map[string]bool{}
	for _, p := range patterns {
		matches, err := filepath.Glob(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", inv.rel(p), err)
		}
		var named, dirs []string
		for _, m := range matches {
			// A glob matches a link whose target is gone as well.
			info, err := os.Stat(m)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", inv.rel(p), err)
			}
			if info.IsDir() {
				dirs = append(dirs, m)
				continue
			}
			named = append(named, m)
		}
		if len(named) == 0 {
			if len(dirs) > 0 {
				return nil, fmt.Errorf("%s names only the directory %s; name its files, such as %s", inv.rel(p), inv.rel(dirs[0]), filepath.Join(inv.rel(dirs[0]), "*.yaml"))
			}
			return nil, fmt.Errorf("%s names no file", inv.rel(p))
		}
		for _, f := range named {
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	return files, nil
}

// read parses a config file once.
func (inv *Inventory) read(file string) ([]config.Document, error) {
	if docs, ok := inv.parsed[file]; ok {
		return docs, nil
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	docs, err := config.Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", inv.rel(file), err)
	}
	inv.parsed[file] = docs
	return docs, nil
}

// rel is a path or glob as messages name it: relative to the inventory's directory where it is
// below it.
func (inv *Inventory) rel(p string) string {
	for _, dir := range []string{inv.Dir(), escapeGlob(inv.Dir())} {
		if r, err := filepath.Rel(dir, p); err == nil && !strings.HasPrefix(r, "..") {
			return r
		}
	}
	return p
}
