package chalklab

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

// defaultLabFile is the lab file chalklab create reads in the current directory when --lab names
// none.
const defaultLabFile = "chalklab.yaml"

// labSchemaFile is the name of the lab file's JSON Schema, as an image names its kinds'.
const labSchemaFile = "chalklab.chalkos.dev_v1alpha1_Lab.json"

//go:embed chalklab.chalkos.dev_v1alpha1_Lab.json
var labSchema []byte

// labFile is a lab file: its name, the inventory of the cluster the lab runs, and the machine of
// each node it runs.
type labFile struct {
	path string
	// name is the lab file's metadata.name, which names the lab's state directory.
	name string
	// inventory is the inventory's absolute path.
	inventory string
	nodes     map[string]labMachine
}

// labMachine is a node's VM as a lab file gives it; zero values take the defaults of the node's
// type.
type labMachine struct {
	MAC    string `json:"mac"`
	Memory int    `json:"memory"`
	CPUs   int    `json:"cpus"`
	Disks  []struct {
		Size   string `json:"size"`
		Serial string `json:"serial"`
	} `json:"disks"`
}

// readLabFile reads a lab file and checks it against its schema; its inventory's path is relative
// to it.
func readLabFile(path string) (*labFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	docs, err := config.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(docs) != 1 {
		return nil, fmt.Errorf("%s holds %d documents; a lab file is one", path, len(docs))
	}
	s, err := schema.LoadFiles(map[string][]byte{labSchemaFile: labSchema})
	if err != nil {
		return nil, err
	}
	if err := s.Validate(docs[0].Object); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	raw, err := json.Marshal(docs[0].Object["spec"])
	if err != nil {
		return nil, err
	}
	var spec struct {
		Inventory string                `json:"inventory"`
		Nodes     map[string]labMachine `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	inv := spec.Inventory
	if !filepath.IsAbs(inv) {
		abs, err := filepath.Abs(filepath.Join(filepath.Dir(path), inv))
		if err != nil {
			return nil, err
		}
		inv = abs
	}
	return &labFile{path: path, name: docs[0].Name, inventory: inv, nodes: spec.Nodes}, nil
}
