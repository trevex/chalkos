package outputs

import (
	"encoding/json"
	"log"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/jsonfile"
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
	st := map[string]Status{}
	if err := jsonfile.Read(statusPath(root), &st); err != nil {
		return nil, err
	}
	return st, nil
}

// RecordFailures records the failures of outputs in the status below root; the hash each output
// serves stays.
func RecordFailures(root string, failures []Failure) error {
	return updateStatus(root, func(st map[string]Status) {
		for _, f := range failures {
			if f.Role != RoleOutput {
				continue
			}
			s := st[f.Name]
			s.Error = f.Problem
			st[f.Name] = s
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
