// Package layers names the layers an image carries beside the base every image has, such as
// Kubernetes. Each layer of an image writes a marker below Dir, named by the layer.
package layers

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"slices"
)

// Dir holds the markers of an image's layers.
const Dir = "/etc/chalkos/layers"

const (
	// Base is what every image has, which needs no marker.
	Base = ""
	// Kubernetes runs containerd and the kubelet and makes a node a control plane or a worker.
	Kubernetes = "kubernetes"
)

// Set is the layers of an image; the zero Set holds the base alone.
type Set map[string]struct{}

// Of is the set of the layers named; the base, which every Set has, takes no entry.
func Of(names ...string) Set {
	s := Set{}
	for _, name := range names {
		if name != Base {
			s[name] = struct{}{}
		}
	}
	return s
}

// Read returns the layers whose markers dir holds: every entry but a directory, as a marker may be
// a symlink. A missing dir holds none.
func Read(dir string) (Set, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Set{}, nil
	}
	if err != nil {
		return nil, err
	}
	s := Set{}
	for _, e := range entries {
		if !e.IsDir() {
			s[e.Name()] = struct{}{}
		}
	}
	return s, nil
}

// Has reports whether the image carries the layer; every image carries Base.
func (s Set) Has(layer string) bool {
	_, ok := s[layer]
	return layer == Base || ok
}

// Names are the layers in the order of their names.
func (s Set) Names() []string {
	return slices.Sorted(maps.Keys(s))
}
