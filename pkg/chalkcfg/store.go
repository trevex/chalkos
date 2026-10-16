// Package chalkcfg is the command that prints a node's config documents, as chalkd writes them
// below /run/chalkos/config for the node's services.
package chalkcfg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// store is where a node's documents are: chalkd writes each below config as
// <group>/<Kind>/<name>.json, and the image names each kind's schema in schemas
// <group>_<version>_<Kind>.json. chalkcfg reads the schemas' names, not their contents, to tell a
// kind the image declares but the node has no documents of from a kind the image does not know.
type store struct {
	config, schemas string
}

// document is a document of the node, by the name of its file.
type document struct {
	kind, name string
	object     map[string]any
}

// kindInfo is what the node has of a kind: the groups it is in, one unless /run was tampered
// with, and its number of documents.
type kindInfo struct {
	groups    []string
	documents int
}

// namePattern is the pattern of metadata.name, which every document's file name follows. Other
// names are no document's, and could reach outside the kind's directory.
var namePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

var errSecret = errors.New("Secrets are never printed: a service gets the values its kinds reference as the systemd credentials secret_<secret>.<key>")

var errAddon = errors.New("Addon documents are never printed: chalkd keeps them in the node's stored config, readable by root alone, and applies their objects on control planes")

// hidden are the kinds chalkcfg never prints, by name, with why.
var hidden = map[string]error{"Secret": errSecret, "Addon": errAddon}

// readDir is os.ReadDir with a directory that does not exist read as empty: a node without config
// has no /run/chalkos/config, and a test may have no schemas.
func readDir(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return entries, err
}

// kinds returns the kinds the image declares or the node has documents of, by name.
func (s store) kinds() (map[string]*kindInfo, error) {
	kinds := map[string]*kindInfo{}
	add := func(kind, group string) *kindInfo {
		k := kinds[kind]
		if k == nil {
			k = &kindInfo{}
			kinds[kind] = k
		}
		if !slices.Contains(k.groups, group) {
			k.groups = append(k.groups, group)
			slices.Sort(k.groups)
		}
		return k
	}
	schemas, err := readDir(s.schemas)
	if err != nil {
		return nil, err
	}
	for _, e := range schemas {
		base, ok := strings.CutSuffix(e.Name(), ".json")
		if parts := strings.Split(base, "_"); ok && len(parts) == 3 {
			add(parts[2], parts[0])
		}
	}
	groups, err := readDir(s.config)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if !g.IsDir() {
			continue
		}
		entries, err := readDir(filepath.Join(s.config, g.Name()))
		if err != nil {
			return nil, err
		}
		for _, k := range entries {
			if !k.IsDir() {
				continue
			}
			names, err := s.names(g.Name(), k.Name())
			if err != nil {
				return nil, err
			}
			add(k.Name(), g.Name()).documents += len(names)
		}
	}
	return kinds, nil
}

// names returns the names of the node's documents of a kind, sorted.
func (s store) names(group, kind string) ([]string, error) {
	entries, err := readDir(filepath.Join(s.config, group, kind))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".json"); ok && !e.IsDir() {
			names = append(names, name)
		}
	}
	return names, nil
}

// group returns the group of a kind named by its name alone.
func (s store) group(kind string) (string, error) {
	if err, ok := hidden[kind]; ok {
		return "", err
	}
	kinds, err := s.kinds()
	if err != nil {
		return "", err
	}
	for kind := range hidden {
		delete(kinds, kind)
	}
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
func (s store) documents(kind, name string) ([]document, error) {
	group, err := s.group(kind)
	if err != nil {
		return nil, err
	}
	names := []string{name}
	if name == "" {
		if names, err = s.names(group, kind); err != nil {
			return nil, err
		}
	} else if !namePattern.MatchString(name) {
		return nil, fmt.Errorf("the node has no %s named %q", kind, name)
	}
	docs := make([]document, 0, len(names))
	for _, n := range names {
		obj, err := readObject(filepath.Join(s.config, group, kind, n+".json"))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("the node has no %s named %q", kind, n)
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, document{kind: kind, name: n, object: obj})
	}
	return docs, nil
}

func readObject(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	// Numbers keep their digits, so a large integer prints as it was written.
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return obj, nil
}

// resolve returns s with its config directory resolved to the directory it links to. chalkd
// replaces the config by pointing /run/chalkos/config at a new directory and removing the old
// one, so a command that resolved the link once reads the kinds and documents of one config, and
// not the kinds of one and the documents of the next. A missing config directory stays as it is.
func (s store) resolve() (store, error) {
	dir, err := filepath.EvalSymlinks(s.config)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return store{}, err
	}
	s.config = dir
	return s, nil
}
