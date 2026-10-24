package outputs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/config"
)

// State is what tells that an output changed: the hash of its files, and the hash of the Secret
// values its input documents reference, which a service reads as credentials beside the files.
type State struct {
	Files   string `json:"files"`
	Secrets string `json:"secrets"`
}

// secrets hashes the values the secretRefs of the output's input documents name, by Secret and
// key; empty for documents that reference none.
func (d Definition) secrets(in Input) string {
	secrets := config.Documents(in.Documents).Secrets()
	var lines []string
	for _, doc := range d.documents(in) {
		for _, ref := range config.SecretRefs(doc, in.Schemas) {
			v, _ := secrets.Lookup(ref)
			sum := sha256.Sum256([]byte(v))
			lines = append(lines, ref.Name+"\x00"+ref.Key+"\x00"+hex.EncodeToString(sum[:]))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	slices.Sort(lines)
	lines = slices.Compact(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// Live is the state of the output the node serves now, before a delivery replaces it: the hash of
// the files its dir holds, empty when it holds none, and the Secrets in reads.
func Live(d Definition, in Input) (State, error) {
	files, err := hashDir(d.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		files, err = "", nil
	}
	if err != nil {
		return State{}, err
	}
	return State{Files: files, Secrets: d.secrets(in)}, nil
}

// hashDir hashes the names and contents of the regular files below dir.
func hashDir(dir string) (string, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	err = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil || !e.Type().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		content := sha256.New()
		if _, err := io.Copy(content, f); err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%x\n", rel, content.Sum(nil))
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
