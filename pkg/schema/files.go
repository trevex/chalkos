package schema

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing/fstest"
)

// LoadFiles compiles schemas given by file name, as a request carries an image directory's
// schemas/, with LoadFS. A name must be the plain name of a .json file.
func LoadFiles(files map[string][]byte) (*Schemas, error) {
	fsys := fstest.MapFS{}
	for name, data := range files {
		if name != filepath.Base(name) || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			return nil, fmt.Errorf("%q is not the name of a schema file", name)
		}
		fsys[name] = &fstest.MapFile{Data: data}
	}
	return LoadFS(fsys)
}
