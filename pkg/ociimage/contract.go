package ociimage

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// Contract is what an image accepts as config and how it lays out a disk: its metadata.json, the
// JSON Schemas of its kinds and its repart definitions of the system region by file name, and the
// facts of its layers by layer name, from facts/<layer>.json; none for a layer without facts.
type Contract struct {
	Metadata    []byte
	Schemas     map[string][]byte
	Definitions map[string][]byte
	Facts       map[string][]byte
}

// maxContract is the largest contract layer ReadContract reads; a real one is a few hundred KiB.
const maxContract = 16 << 20

// ReadContractDir reads the contract of an image directory, as nix build makes it: metadata.json,
// schemas/*.json, repart.d/*.conf and facts/*.json.
func ReadContractDir(dir string) (Contract, error) {
	var c Contract
	var err error
	if c.Metadata, err = os.ReadFile(filepath.Join(dir, "metadata.json")); err != nil {
		return Contract{}, err
	}
	if c.Schemas, err = readFiles(filepath.Join(dir, "schemas"), "*.json"); err != nil {
		return Contract{}, err
	}
	if c.Definitions, err = readFiles(filepath.Join(dir, "repart.d"), "*.conf"); err != nil {
		return Contract{}, err
	}
	files, err := readFiles(filepath.Join(dir, "facts"), "*.json")
	if err != nil {
		return Contract{}, err
	}
	for name, data := range files {
		c.setFact(strings.TrimSuffix(name, ".json"), data)
	}
	if err := c.check(); err != nil {
		return Contract{}, fmt.Errorf("%s: %w", dir, err)
	}
	return c, nil
}

// ReadFile reads a file of the contract by its name in the image's directory: a layer's
// facts/<layer>.json alone, which is fs.ErrNotExist for a layer without facts.
func (c Contract) ReadFile(name string) ([]byte, error) {
	dir, base := path.Split(name)
	if dir == "facts/" && path.Ext(base) == ".json" {
		if data, ok := c.Facts[strings.TrimSuffix(base, ".json")]; ok {
			return data, nil
		}
	}
	return nil, fs.ErrNotExist
}

// setFact sets the facts of a layer; Facts stays nil for a contract without any.
func (c *Contract) setFact(layer string, data []byte) {
	if c.Facts == nil {
		c.Facts = map[string][]byte{}
	}
	c.Facts[layer] = data
}

// readFiles reads the files of dir whose names match pattern, by name.
func readFiles(dir, pattern string) (map[string][]byte, error) {
	paths, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		files[filepath.Base(p)] = data
	}
	return files, nil
}

// check refuses a contract without metadata.json, schemas or repart definitions, and one with
// facts/.json, the facts of a layer without a name.
func (c Contract) check() error {
	_, unnamed := c.Facts[""]
	switch {
	case len(c.Metadata) == 0:
		return errors.New("the contract has no metadata.json")
	case len(c.Schemas) == 0:
		return errors.New("the contract has no schemas/*.json")
	case len(c.Definitions) == 0:
		return errors.New("the contract has no repart.d/*.conf")
	case unnamed:
		return errors.New("the contract has facts/.json, the facts of a layer without a name")
	}
	return nil
}

// Tar is the contract as its layer holds it: a tar of metadata.json, schemas/, repart.d/ and
// facts/ in name order, of regular files of mode 0644, owned by 0:0 and modified at the epoch,
// so that the same contract always makes the same layer.
func (c Contract) Tar() ([]byte, error) {
	files := map[string][]byte{"metadata.json": c.Metadata}
	for layer, data := range c.Facts {
		files["facts/"+layer+".json"] = data
	}
	for name, data := range c.Schemas {
		files["schemas/"+name] = data
	}
	for name, data := range c.Definitions {
		files["repart.d/"+name] = data
	}
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		h := &tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(files[name])), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}
		if err := w.WriteHeader(h); err != nil {
			return nil, err
		}
		if _, err := w.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ParseContract reads a contract layer. It skips directories and files outside the contract, so
// that a later chalkos may add some, and refuses links, other special files and a name it has
// already read.
func ParseContract(r io.Reader) (Contract, error) {
	c := Contract{Schemas: map[string][]byte{}, Definitions: map[string][]byte{}}
	var haveMetadata bool
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Contract{}, fmt.Errorf("the contract layer: %w", err)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return Contract{}, fmt.Errorf("the contract layer holds %s, which is no regular file", h.Name)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return Contract{}, fmt.Errorf("the contract layer: %w", err)
		}
		dir, base := path.Split(path.Clean(h.Name))
		switch {
		case dir == "" && base == "metadata.json":
			if haveMetadata {
				return Contract{}, fmt.Errorf("the contract layer holds %s twice", base)
			}
			haveMetadata = true
			c.Metadata = data
		case dir == "facts/" && path.Ext(base) == ".json":
			layer := strings.TrimSuffix(base, ".json")
			if _, dup := c.Facts[layer]; dup {
				return Contract{}, fmt.Errorf("the contract layer holds %s twice", dir+base)
			}
			c.setFact(layer, data)
		case dir == "schemas/" && path.Ext(base) == ".json":
			if _, dup := c.Schemas[base]; dup {
				return Contract{}, fmt.Errorf("the contract layer holds %s twice", dir+base)
			}
			c.Schemas[base] = data
		case dir == "repart.d/" && path.Ext(base) == ".conf":
			if _, dup := c.Definitions[base]; dup {
				return Contract{}, fmt.Errorf("the contract layer holds %s twice", dir+base)
			}
			c.Definitions[base] = data
		}
	}
	if err := c.check(); err != nil {
		return Contract{}, err
	}
	return c, nil
}

// ReadContract reads the contract layer of a chalkos image.
//
//   - It refuses a layer larger than a contract may be by its descriptor's size, before reading it.
//   - It reads a smaller layer to its end before parsing it. go-containerregistry checks a remote
//     blob's digest only at end of stream, and archive/tar stops at its end-of-archive marker, so
//     parsing straight from the layer's reader would accept a layer tampered with in transit.
func ReadContract(img v1.Image) (Contract, error) {
	m, err := img.Manifest()
	if err != nil {
		return Contract{}, err
	}
	d, err := find(m, ContractType)
	if err != nil {
		return Contract{}, err
	}
	if d.Size > maxContract {
		return Contract{}, fmt.Errorf("its contract layer is %d bytes, more than the %d a contract may be", d.Size, maxContract)
	}
	l, err := img.LayerByDigest(d.Digest)
	if err != nil {
		return Contract{}, err
	}
	rc, err := l.Compressed()
	if err != nil {
		return Contract{}, err
	}
	data, err := io.ReadAll(io.LimitReader(rc, maxContract+1))
	if cerr := rc.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Contract{}, fmt.Errorf("its contract layer: %w", err)
	}
	if int64(len(data)) > maxContract {
		return Contract{}, fmt.Errorf("its contract layer is more than the %d bytes a contract may be", maxContract)
	}
	return ParseContract(bytes.NewReader(data))
}
