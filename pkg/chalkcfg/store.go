// Package chalkcfg is the command that prints a node's config documents, as chalkd writes them
// below /run/chalkos/config for the node's services.
package chalkcfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/config/slot"
	"github.com/trevex/chalkos/pkg/schema"
)

// store is where a node's documents are: chalkd writes them below run, as slot.WriteRuntime
// does, and the image keeps each kind's schema in schemas. chalkcfg reads the schemas' names to
// tell a kind the image declares but the node has no documents of from a kind the image does not
// know, and of their contents whether the kind is withheld.
type store struct {
	run, schemas string
}

// node is what chalkcfg read of a store at once: the kinds by name and the documents.
type node struct {
	kinds map[string]*kindInfo
	docs  config.Documents
}

// kindInfo is what the node has of a kind: the groups it is in, one unless /run was tampered
// with, its number of documents, and whether a schema of the kind declares it withheld.
type kindInfo struct {
	groups    []string
	documents int
	withheld  bool
}

var errSecret = errors.New("Secrets are never printed: a service gets the values its kinds reference as the systemd credentials secret_<secret>.<key>")

// errWithheld is why chalkcfg prints no document of a kind the image declares withheld.
var errWithheld = errors.New("the image withholds the kind from the node's services, and chalkd alone reads its documents from the node's stored config, readable by root alone")

// secretKind is the kind of Secrets, which chalkcfg never prints.
const secretKind = "Secret"

// read reads the kinds the image declares and the node's documents. chalkd replaces the config
// by pointing /run/chalkos/config at a new directory and removing the old one; slot.ReadRuntime
// resolves the link once, so the documents read are those of one config. Every command reads
// every document below the directory, whichever kind it prints, so a file chalkd wrote that does
// not decode fails every command.
func (s store) read() (node, error) {
	docs, err := slot.ReadRuntime(s.run)
	if err != nil {
		return node{}, err
	}
	n := node{kinds: map[string]*kindInfo{}, docs: docs}
	add := func(apiVersion, kind string) *kindInfo {
		k := n.kinds[kind]
		if k == nil {
			k = &kindInfo{}
			n.kinds[kind] = k
		}
		if g := groupOf(apiVersion); !slices.Contains(k.groups, g) {
			k.groups = append(k.groups, g)
			slices.Sort(k.groups)
		}
		return k
	}
	// A test may have no schemas.
	declared, err := schema.KindsInDir(s.schemas)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return node{}, err
	}
	for _, d := range declared {
		withheld, err := withheldSchema(filepath.Join(s.schemas, schema.FileName(d)))
		if err != nil {
			return node{}, err
		}
		k := add(d.APIVersion, d.Kind)
		k.withheld = k.withheld || withheld
	}
	for _, d := range docs {
		add(d.APIVersion, d.Kind).documents++
	}
	return n, nil
}

// groupOf is the group of apiVersion.
func groupOf(apiVersion string) string {
	g, _, _ := strings.Cut(apiVersion, "/")
	return g
}

// group returns the group of a kind named by its name alone.
func (n node) group(kind string) (string, error) {
	if kind == secretKind {
		return "", errSecret
	}
	if k, ok := n.kinds[kind]; ok && k.withheld {
		return "", fmt.Errorf("%s documents are never printed: %w", kind, errWithheld)
	}
	kinds := maps.Clone(n.kinds)
	maps.DeleteFunc(kinds, withheldKind)
	k, ok := kinds[kind]
	if !ok {
		return "", fmt.Errorf("the image declares no kind %s; it declares %s", kind, strings.Join(slices.Sorted(maps.Keys(kinds)), ", "))
	}
	if len(k.groups) > 1 {
		return "", fmt.Errorf("the kind %s is in more than one group (%s), and chalkcfg names kinds by their names alone", kind, strings.Join(k.groups, ", "))
	}
	return k.groups[0], nil
}

// documents returns the node's document of a kind with the given name, or, for an empty name, all
// its documents of the kind in name order. A declared kind of which the node has no documents
// gives none and no error.
func (n node) documents(kind, name string) ([]config.Document, error) {
	g, err := n.group(kind)
	if err != nil {
		return nil, err
	}
	var docs []config.Document
	for _, d := range n.docs {
		if d.Kind == kind && groupOf(d.APIVersion) == g && (name == "" || d.Name == name) {
			docs = append(docs, d)
		}
	}
	if name != "" && len(docs) == 0 {
		return nil, fmt.Errorf("the node has no %s named %q", kind, name)
	}
	return docs, nil
}

// withheldKind tells whether kind is Secret or withheld, for maps.DeleteFunc on the kinds by name.
func withheldKind(kind string, k *kindInfo) bool { return kind == secretKind || k.withheld }

// withheldSchema tells whether the kind's schema at path declares the kind withheld, by the
// withheld field of its x-chalkos-kind.
func withheldSchema(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var sch struct {
		Kind struct {
			Withheld bool `json:"withheld"`
		} `json:"x-chalkos-kind"`
	}
	if err := json.Unmarshal(data, &sch); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return sch.Kind.Withheld, nil
}
