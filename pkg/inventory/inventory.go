// Package inventory reads chalkctl's inventory: the file that names a cluster's nodes, the
// address chalkctl reaches each at, and the config files each node gets. Paths in an inventory
// are relative to its directory, and globs are allowed.
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

// Spec is what an inventory says of its cluster's nodes.
type Spec struct {
	// Secrets is the secrets file; empty for secrets.age, else secrets.json, beside the inventory.
	Secrets    string      `json:"secrets,omitempty"`
	SecureBoot *SecureBoot `json:"secureBoot,omitempty"`
	// Files are the config files every node gets, as paths or globs.
	Files  []string         `json:"files,omitempty"`
	Groups map[string]Group `json:"groups,omitempty"`
	Nodes  map[string]Node  `json:"nodes,omitempty"`
}

// SecureBoot names the certificate of the Secure Boot db key the nodes' images are signed with.
type SecureBoot struct {
	SignerCertificate string `json:"signerCertificate"`
}

// Group is config files that the nodes in the group get.
type Group struct {
	Files []string `json:"files,omitempty"`
}

// Node is a node of the cluster: where chalkctl reaches it, the groups it is in and its own
// config files.
type Node struct {
	Address string   `json:"address,omitempty"`
	Groups  []string `json:"groups,omitempty"`
	Files   []string `json:"files,omitempty"`
}

// Inventory is an inventory as chalkctl reads it: its spec with every path absolute.
type Inventory struct {
	// Path is the inventory file's absolute path.
	Path string
	Name string
	Spec Spec
	// parsed holds the documents of each config file read so far, by path.
	parsed map[string][]config.Document
}

// Load reads an inventory and checks it against its schema and its own references: every group a
// node is in must be defined.
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
// schema, with its paths made absolute against its directory.
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
	spec.Secrets = absolute(dir, spec.Secrets)
	if spec.SecureBoot != nil {
		spec.SecureBoot = &SecureBoot{SignerCertificate: absolute(dir, spec.SecureBoot.SignerCertificate)}
	}
	spec.Files = absoluteAll(dir, spec.Files)
	groups := map[string]Group{}
	for name, g := range spec.Groups {
		groups[name] = Group{Files: absoluteAll(dir, g.Files)}
	}
	nodes := map[string]Node{}
	for _, name := range slices.Sorted(maps.Keys(spec.Nodes)) {
		n := spec.Nodes[name]
		for _, g := range n.Groups {
			if _, ok := groups[g]; !ok {
				return nil, fmt.Errorf("node %s is in the group %s, which the inventory does not define", name, g)
			}
		}
		n.Files = absoluteAll(dir, n.Files)
		nodes[name] = n
	}
	spec.Groups, spec.Nodes = groups, nodes
	return &Inventory{Path: path, Name: d.Metadata.Name, Spec: spec, parsed: map[string][]config.Document{}}, nil
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

func absoluteAll(dir string, paths []string) []string {
	var out []string
	for _, p := range paths {
		out = append(out, absolute(dir, p))
	}
	return out
}

// Dir is the inventory's directory.
func (inv *Inventory) Dir() string { return filepath.Dir(inv.Path) }

// NodeNames are the inventory's nodes, sorted.
func (inv *Inventory) NodeNames() []string { return slices.Sorted(maps.Keys(inv.Spec.Nodes)) }

// Files are the config files of a node, in the order their documents form its set: the shared
// files, then its groups' files in the order it names the groups, then its own. Each path or glob
// must name at least one file; a glob's files come in name order.
func (inv *Inventory) Files(node string) ([]string, error) {
	n, ok := inv.Spec.Nodes[node]
	if !ok {
		return nil, fmt.Errorf("the inventory %s has no node %s; its nodes are %s", inv.Path, node, strings.Join(inv.NodeNames(), ", "))
	}
	patterns := slices.Clone(inv.Spec.Files)
	for _, g := range n.Groups {
		patterns = append(patterns, inv.Spec.Groups[g].Files...)
	}
	patterns = append(patterns, n.Files...)
	return inv.expand(patterns)
}

// expand lists the files of paths and globs, each of which must name one at least.
func (inv *Inventory) expand(patterns []string) ([]string, error) {
	var files []string
	for _, p := range patterns {
		matches, err := filepath.Glob(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", inv.rel(p), err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("%s names no file", inv.rel(p))
		}
		files = append(files, matches...)
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

// rel is a path as messages name it: relative to the inventory's directory where it is below it.
func (inv *Inventory) rel(p string) string {
	if r, err := filepath.Rel(inv.Dir(), p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}
