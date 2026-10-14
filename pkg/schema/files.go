package schema

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadFiles compiles schemas given by file name, as a request carries an image directory's
// schemas/, with LoadSchemas. A name must be the plain name of a .json file, so no file lands
// outside the temporary directory they are written to.
func LoadFiles(files map[string][]byte) (*Schemas, error) {
	dir, err := os.MkdirTemp("", "chalkos-schemas-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	for name, data := range files {
		if name != filepath.Base(name) || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			return nil, fmt.Errorf("%q is not the name of a schema file", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return nil, err
		}
	}
	return LoadSchemas(dir)
}
