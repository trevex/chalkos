package chalkd

import (
	"os"
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

// A failed delivery's attempt goes to the baseline once, however often it is retried, and a
// baseline an older chalkd wrote, without attempts, reads as one with none.
func TestBaselineAddsAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(path, []byte(`{"documents": [], "facts": {}, "outputs": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := baselineAttempt{Documents: []map[string]any{{"kind": "Demo"}}, Kinds: map[string]string{"Demo": "abc"}}
	for range 2 {
		if err := addAttempt(path, a); err != nil {
			t.Fatal(err)
		}
	}
	b, _, err := readBaseline(path)
	if err != nil || len(b.Attempts) != 1 || !reflect.DeepEqual(b.Attempts[0].Kinds, a.Kinds) {
		t.Errorf("baseline = %+v, %v; want the attempt once", b, err)
	}
	if err := addAttempt(filepath.Join(t.TempDir(), "none.json"), a); err == nil {
		t.Error("an attempt without a baseline was recorded")
	}
}
