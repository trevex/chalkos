package upgrade

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
)

// ImagesRecord is where STATE keeps the provenance record, below its mount point.
const ImagesRecord = "chalkd/images.json"

// Provenance is what a build says of where it came from: the git revision and tag of the flake
// it was built from. It is shown beside an image's root hash and decides nothing: two builds of
// one revision may differ, and one store may come from many revisions.
type Provenance struct {
	Revision string `json:"revision,omitempty"`
	Tag      string `json:"tag,omitempty"`
}

// Images is the provenance record: the provenance of each image by the root hash of its store, in
// hex.
type Images map[string]Provenance

// ReadImages reads the provenance record at path. A record that is missing or cannot be read is
// empty: it is shown, never relied on.
func ReadImages(path string) Images {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Images{}
	}
	var images Images
	if err == nil {
		err = json.Unmarshal(data, &images)
	}
	if err != nil || images == nil {
		log.Printf("read the record of the images' provenance, %s: %v", path, err)
		return Images{}
	}
	return images
}

// Of is the provenance of the image whose store has the root hash; empty when the record does not
// know it.
func (im Images) Of(rootHash []byte) Provenance {
	return im[hex.EncodeToString(rootHash)]
}

// Keep forgets every image but those whose root hashes keep names, the stores the UKIs on the ESP
// boot.
func (im Images) Keep(keep [][]byte) Images {
	out := Images{}
	for _, k := range keep {
		if old, ok := im[hex.EncodeToString(k)]; ok {
			out[hex.EncodeToString(k)] = old
		}
	}
	return out
}

// Record adds the image's provenance to the record and forgets every other image but those whose
// root hashes keep names, as Keep does. A build that names no revision or tag keeps what the
// record knew of its store.
func (im Images) Record(rootHash []byte, p Provenance, keep [][]byte) Images {
	out := im.Keep(keep)
	id := hex.EncodeToString(rootHash)
	if old, ok := im[id]; ok {
		out[id] = old
	}
	if p != (Provenance{}) {
		out[id] = p
	}
	return out
}

// Encode is the record as images.json holds it.
func (im Images) Encode() ([]byte, error) {
	data, err := json.MarshalIndent(im, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
