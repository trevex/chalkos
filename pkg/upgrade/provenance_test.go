package upgrade

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestImagesRecord keeps each image's provenance by its root hash: written as images.json, pruned
// to the stores the ESP's UKIs boot, kept for a build that names none, and empty when the file is
// missing or cannot be read.
func TestImagesRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), ImagesRecord)
	if im := ReadImages(path); im == nil || len(im) != 0 {
		t.Errorf("a missing record = %v, want an empty one", im)
	}
	a, b, c := bytes.Repeat([]byte{0xaa}, 32), bytes.Repeat([]byte{0xbb}, 32), bytes.Repeat([]byte{0xcc}, 32)
	im := Images{}.Record(a, Provenance{Revision: "r1", Tag: "v1"}, nil)
	im = im.Record(b, Provenance{Revision: "r2"}, [][]byte{a, b})
	data, err := im.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if want := `"` + hex.EncodeToString(a) + `": {` + "\n" + `    "revision": "r1",` + "\n" + `    "tag": "v1"`; !strings.Contains(string(data), want) {
		t.Errorf("images.json:\n%s\nwant it to hold\n%s", data, want)
	}
	read := ReadImages(path)
	if read.Of(a) != (Provenance{Revision: "r1", Tag: "v1"}) || read.Of(b) != (Provenance{Revision: "r2"}) || read.Of(c) != (Provenance{}) {
		t.Errorf("read %v", read)
	}

	// An upgrade to c, after which the ESP's UKIs boot b and c: a is forgotten.
	im = read.Record(c, Provenance{Tag: "v3"}, [][]byte{b, c})
	want := Images{hex.EncodeToString(b): {Revision: "r2"}, hex.EncodeToString(c): {Tag: "v3"}}
	if !reflect.DeepEqual(im, want) {
		t.Errorf("after the upgrade %v, want %v", im, want)
	}
	// A build that names no provenance keeps what the record knew of its store.
	if got := im.Record(b, Provenance{}, [][]byte{b, c}); !reflect.DeepEqual(got, want) {
		t.Errorf("after a build without provenance %v, want %v", got, want)
	}
	if got := im.Record(a, Provenance{}, [][]byte{a, c}); !reflect.DeepEqual(got, Images{hex.EncodeToString(c): {Tag: "v3"}}) {
		t.Errorf("after an unknown build without provenance %v", got)
	}

	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if im := ReadImages(path); im == nil || len(im) != 0 {
		t.Errorf("an unreadable record = %v, want an empty one", im)
	}
}
