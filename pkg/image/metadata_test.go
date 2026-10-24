package image

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProvenance names the revision and tag of metadata.json only when it names the root hash
// given, in a schema version it reads. Metadata of another root hash or schema version, or that
// is not valid JSON, returns an error; a directory without metadata.json returns none of either.
func TestProvenance(t *testing.T) {
	root := bytes.Repeat([]byte{0xcd}, 32)
	hexRoot := hex.EncodeToString(root)
	for _, tc := range []struct {
		name, metadata string
		want           bool
	}{
		{"its root hash", `{"schemaVersion": 0, "rootHash": "` + hexRoot + `", "revision": "a1b2c3", "tag": "v1.5.0"}`, true},
		{"its root hash in upper case", `{"schemaVersion": 0, "rootHash": "` + strings.ToUpper(hexRoot) + `", "revision": "a1b2c3", "tag": "v1.5.0"}`, true},
		{"another root hash", `{"schemaVersion": 0, "rootHash": "` + strings.Repeat("ab", 32) + `", "revision": "a1b2c3", "tag": "v1.5.0"}`, false},
		{"its root hash and another schema version", `{"schemaVersion": 9, "rootHash": "` + hexRoot + `", "revision": "a1b2c3", "tag": "v1.5.0"}`, false},
		{"another root hash and schema version", `{"schemaVersion": 9, "rootHash": "` + strings.Repeat("ab", 32) + `", "revision": "a1b2c3", "tag": "v1.5.0"}`, false},
		{"no root hash", `{"schemaVersion": 0, "revision": "a1b2c3", "tag": "v1.5.0"}`, false},
		{"no JSON", `revision=a1b2c3`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(tc.metadata), 0o644); err != nil {
				t.Fatal(err)
			}
			revision, tag, err := Provenance(dir, root)
			if tc.want {
				if err != nil || revision != "a1b2c3" || tag != "v1.5.0" {
					t.Errorf("Provenance = %q, %q, %v; want a1b2c3, v1.5.0, nil", revision, tag, err)
				}
				return
			}
			if err == nil || revision != "" || tag != "" {
				t.Errorf("Provenance = %q, %q, %v; want empty strings and an error", revision, tag, err)
			}
		})
	}
	dir := t.TempDir()
	revision, tag, err := Provenance(dir, root)
	if err != nil || revision != "" || tag != "" {
		t.Errorf("Provenance without metadata.json = %q, %q, %v; want empty strings and nil", revision, tag, err)
	}
}
