package chalkd

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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
// baseline an older chalkd wrote, without attempts, reads as one with none. The facts the attempt
// loaded once it changed the node go to it later; a retry records them again.
func TestBaselineAddsAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(path, []byte(`{"documents": [], "facts": {}, "outputs": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := baselineAttempt{Documents: []map[string]any{{"kind": "Demo"}}, Kinds: map[string]string{"Demo": "abc"}}
	facts := recordedFacts{"node": {Value: "h2"}}
	for range 2 {
		if err := addAttempt(path, a); err != nil {
			t.Fatal(err)
		}
		if b, _, _ := readBaseline(path); len(b.Attempts) != 1 || b.Attempts[0].Facts != nil {
			t.Fatalf("baseline = %+v; want the attempt once, without facts", b)
		}
		if err := setAttemptFacts(path, a, facts); err != nil {
			t.Fatal(err)
		}
	}
	b, _, err := readBaseline(path)
	if err != nil || len(b.Attempts) != 1 || !reflect.DeepEqual(b.Attempts[0].Kinds, a.Kinds) || !reflect.DeepEqual(b.Attempts[0].Facts, facts) {
		t.Errorf("baseline = %+v, %v; want the attempt once, with its facts", b, err)
	}
	if err := addAttempt(filepath.Join(t.TempDir(), "none.json"), a); err == nil {
		t.Error("an attempt without a baseline was recorded")
	}
}

// A baseline keeps the newest attempts alone, the one retried last among them, and no attempt
// that applies what the baseline holds, as a failed delivery of the baseline's config does.
func TestBaselineKeepsTheNewestAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	kinds := map[string]string{"Demo": "abc"}
	if err := newBaseline(nil, nil, nil, nil, kinds).write(path); err != nil {
		t.Fatal(err)
	}
	if err := addAttempt(path, baselineAttempt{Kinds: kinds}); err != nil {
		t.Fatal(err)
	}
	if b, _, _ := readBaseline(path); len(b.Attempts) != 0 {
		t.Errorf("attempts = %+v, want none for the baseline's own state", b.Attempts)
	}
	attempt := func(i int) baselineAttempt {
		return baselineAttempt{Kinds: map[string]string{"Demo": strconv.Itoa(i)}}
	}
	for i := range maxAttempts + 2 {
		if err := addAttempt(path, attempt(i)); err != nil {
			t.Fatal(err)
		}
	}
	// A retry of the oldest attempt kept makes it the newest.
	if err := addAttempt(path, attempt(2)); err != nil {
		t.Fatal(err)
	}
	b, _, err := readBaseline(path)
	if err != nil || len(b.Attempts) != maxAttempts {
		t.Fatalf("baseline = %+v, %v; want %d attempts", b, err, maxAttempts)
	}
	if first, last := b.Attempts[0].Kinds["Demo"], b.Attempts[maxAttempts-1].Kinds["Demo"]; first != "3" || last != "2" {
		t.Errorf("attempts run from %s to %s, want from 3 to 2", first, last)
	}
}
