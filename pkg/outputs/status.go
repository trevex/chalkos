package outputs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/atomicfile"
)

// Status is what a node reports of an output: the hash of the files it serves, and why its last
// render or check failed, empty once one passed.
type Status struct {
	Hash  string `json:"hash,omitempty"`
	Error string `json:"error,omitempty"`
}

func statusPath(root string) string { return filepath.Join(root, ".status.json") }

// ReadStatus reads the outputs' status below root; none was recorded on a node that rendered
// none.
func ReadStatus(root string) (map[string]Status, error) {
	data, err := os.ReadFile(statusPath(root))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Status{}, nil
	}
	if err != nil {
		return nil, err
	}
	st := map[string]Status{}
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", statusPath(root), err)
	}
	return st, nil
}

// RecordFailures records the failures in the status below root; the hash each output serves
// stays.
func RecordFailures(root string, failures []Failure) error {
	return updateStatus(root, func(st map[string]Status) {
		for _, f := range failures {
			s := st[f.Output]
			s.Error = f.Problem
			st[f.Output] = s
		}
	})
}

func updateStatus(root string, update func(map[string]Status)) error {
	st, err := ReadStatus(root)
	if err != nil {
		// A status that cannot be read is replaced.
		log.Printf("the outputs' status: %v", err)
		st = map[string]Status{}
	}
	update(st)
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(statusPath(root), data, 0o644)
}
