package chalkd

import (
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

// A failed delivery's attempt goes to the baseline before it changes anything, and a baseline an
// older chalkd wrote, without attempts, reads as one with none. The facts the attempt loaded once
// it changed the node go to it later. A retry that loads the same facts leaves the attempt once;
// one that loads others keeps both runs.
func TestBaselineAddsAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	older := `{"documents": [], "facts": {}, "outputs": {}}`
	write(t, path, older)
	a := baselineAttempt{Documents: []map[string]any{{"kind": "Demo"}}, Kinds: map[string]string{"Demo": "abc"}}
	facts := recordedFacts{"node": {Value: "h2"}}
	for i := range 2 {
		previous, err := addAttempt(path, a)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && string(previous) != older {
			t.Errorf("previous = %s, want %s", previous, older)
		}
		if b, _, _ := readBaseline(path); len(b.Attempts) != i+1 || b.Attempts[i].Facts != nil {
			t.Fatalf("baseline = %+v; want %d attempts, the newest without facts", b, i+1)
		}
		if err := setAttemptFacts(path, a, facts); err != nil {
			t.Fatal(err)
		}
	}
	b, _, err := readBaseline(path)
	if err != nil || len(b.Attempts) != 1 || !reflect.DeepEqual(b.Attempts[0].Kinds, a.Kinds) || !reflect.DeepEqual(b.Attempts[0].Facts, facts) {
		t.Fatalf("baseline = %+v, %v; want the attempt once, with its facts", b, err)
	}
	other := recordedFacts{"node": {Value: "h3"}}
	if _, err := addAttempt(path, a); err != nil {
		t.Fatal(err)
	}
	if err := setAttemptFacts(path, a, other); err != nil {
		t.Fatal(err)
	}
	b, _, err = readBaseline(path)
	if err != nil || len(b.Attempts) != 2 || !reflect.DeepEqual(b.Attempts[0].Facts, facts) || !reflect.DeepEqual(b.Attempts[1].Facts, other) {
		t.Errorf("baseline = %+v, %v; want both runs of the attempt with their facts", b, err)
	}
	if _, err := addAttempt(filepath.Join(t.TempDir(), "none.json"), a); err == nil {
		t.Error("an attempt without a baseline was recorded")
	}
}

// The facts go to the newest attempt alone, the one the delivery added; facts for another are
// refused and recorded nowhere.
func TestBaselineSetsTheFactsOfTheNewestAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := newBaseline(nil, nil, nil, nil, nil).write(path); err != nil {
		t.Fatal(err)
	}
	a := baselineAttempt{Documents: []map[string]any{{"kind": "Demo", "spec": map[string]any{"ratio": 0.1, "count": 3}}}}
	b := baselineAttempt{Kinds: map[string]string{"Demo": "abc"}}
	for _, at := range []baselineAttempt{a, b} {
		if _, err := addAttempt(path, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := setAttemptFacts(path, a, recordedFacts{"node": {Value: "h2"}}); err == nil {
		t.Error("the facts of an attempt older than the newest were recorded")
	}
	if _, err := addAttempt(path, a); err != nil {
		t.Fatal(err)
	}
	facts := recordedFacts{"node": {Value: "h2"}}
	if err := setAttemptFacts(path, a, facts); err != nil {
		t.Fatal(err)
	}
	got, _, err := readBaseline(path)
	if err != nil || len(got.Attempts) != 3 || got.Attempts[0].Facts != nil || !reflect.DeepEqual(got.Attempts[2].Facts, facts) {
		t.Errorf("baseline = %+v, %v; want the facts on the newest attempt alone", got, err)
	}
}

// A baseline keeps the newest attempts alone, the one retried last among them, and an attempt
// that applies what the baseline holds, as a delivery that changes a fact alone does.
func TestBaselineKeepsTheNewestAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	kinds := map[string]string{"Demo": "abc"}
	if err := newBaseline(nil, nil, nil, nil, kinds).write(path); err != nil {
		t.Fatal(err)
	}
	if _, err := addAttempt(path, baselineAttempt{Kinds: kinds}); err != nil {
		t.Fatal(err)
	}
	if b, _, _ := readBaseline(path); len(b.Attempts) != 1 {
		t.Errorf("attempts = %+v, want the one of the baseline's own state", b.Attempts)
	}
	attempt := func(i int) baselineAttempt {
		return baselineAttempt{Kinds: map[string]string{"Demo": strconv.Itoa(i)}}
	}
	facts := recordedFacts{"node": {Value: "h2"}}
	add := func(i int, f recordedFacts) {
		t.Helper()
		if _, err := addAttempt(path, attempt(i)); err != nil {
			t.Fatal(err)
		}
		if err := setAttemptFacts(path, attempt(i), f); err != nil {
			t.Fatal(err)
		}
	}
	for i := range maxAttempts + 2 {
		add(i, facts)
	}
	// A retry with the same facts moves the attempt to the newest; one with other facts keeps
	// the earlier run as well.
	add(5, facts)
	add(6, recordedFacts{"node": {Value: "h3"}})
	b, _, err := readBaseline(path)
	if err != nil || len(b.Attempts) != maxAttempts {
		t.Fatalf("baseline = %+v, %v; want %d attempts", b, err, maxAttempts)
	}
	var order []string
	for _, a := range b.Attempts {
		order = append(order, a.Kinds["Demo"])
	}
	want := []string{"3", "4", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15", "16", "17", "5", "6"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("attempts = %v, want %v", order, want)
	}
}
