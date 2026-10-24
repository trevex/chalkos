package schema

import (
	"fmt"
	"path/filepath"
	"strings"
)

// LoadFiles compiles schemas given by file name, as a request carries an image directory's
// schemas/. A name must be the plain name of a .json file; errors name the files by it.
func LoadFiles(files map[string][]byte) (*Schemas, error) {
	for name := range files {
		if name != filepath.Base(name) || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			return nil, fmt.Errorf("%q is not the name of a schema file", name)
		}
	}
	return compile(files, "")
}
