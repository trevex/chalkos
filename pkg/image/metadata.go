package image

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/trevex/chalkos/pkg/schema"
)

// Provenance is the revision and tag the metadata.json in dir names, when the image directory
// describes the image of the root hash. A raw image may lie next to the metadata of another, such
// as one copied into an older build's directory, so metadata of another root hash, or metadata
// that cannot be read, names none, and the error says why. An image without metadata, as one
// built by hand, names none without an error.
func Provenance(dir string, rootHash []byte) (revision, tag string, err error) {
	data, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	var md schema.Metadata
	if err := json.Unmarshal(data, &md); err != nil {
		return "", "", err
	}
	if !strings.EqualFold(md.RootHash, hex.EncodeToString(rootHash)) {
		return "", "", fmt.Errorf("it names the root hash %q, not the image's", md.RootHash)
	}
	return md.Revision, md.Tag, nil
}
