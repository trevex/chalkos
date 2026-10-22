package chalkd

import (
	"path/filepath"
	"reflect"
	"testing"
)

// The baseline records the hashes of the kinds the node ran, which a retry compares with.
func TestBaselineRecordsKinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	kinds := map[string]string{"Demo": "abc"}
	if err := newBaseline(nil, nil, nil, nil, kinds).write(path); err != nil {
		t.Fatal(err)
	}
	b, found, err := readBaseline(path)
	if err != nil || !found || !reflect.DeepEqual(b.Kinds, kinds) {
		t.Errorf("baseline = %+v, %v, %v; want kinds %v", b, found, err, kinds)
	}
}
