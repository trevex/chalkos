package clustersecrets

import (
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	readAt   = time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	laterAt  = readAt.Add(time.Hour)
	recordAt = readAt.Add(2 * time.Hour)
)

// TestRecordRefreshes drops the revocations tried that are not left, lists those left once,
// recorded now, and keeps the others, which another command recorded.
func TestRecordRefreshes(t *testing.T) {
	s := Secrets{Unrefreshed: []Unrefreshed{
		{Layer: "kubernetes", Node: "w1", Listed: true, Recorded: readAt},
		{Layer: "kubernetes", Node: "w2", Recorded: readAt},
		{Layer: "other", Node: "w1", Recorded: readAt},
	}}
	before := slices.Clone(s.Unrefreshed)
	changed := s.RecordRefreshes(
		[]Unrefreshed{{Layer: "kubernetes", Node: "w1", Recorded: readAt}, {Layer: "kubernetes", Node: "w2", Recorded: readAt}, {Layer: "kubernetes", Node: "w3"}},
		[]Unrefreshed{{Layer: "kubernetes", Node: "w2", Listed: true}, {Layer: "kubernetes", Node: "w3"}},
		recordAt,
	)
	want := []Unrefreshed{
		{Layer: "kubernetes", Node: "w2", Listed: true, Recorded: recordAt},
		{Layer: "kubernetes", Node: "w3", Recorded: recordAt},
		{Layer: "other", Node: "w1", Recorded: readAt},
	}
	if !changed || !slices.Equal(s.Unrefreshed, want) {
		t.Errorf("changed %v, recorded %v; want true and %v", changed, s.Unrefreshed, want)
	}
	if before[0].Node != "w1" || !before[0].Listed {
		t.Error("RecordRefreshes changed the slice it replaced")
	}
	if s.RecordRefreshes(nil, nil, recordAt) {
		t.Error("recording nothing changed the record")
	}
	s.RecordRefreshes(want, nil, recordAt)
	if s.Unrefreshed != nil {
		t.Errorf("recorded %v, want none", s.Unrefreshed)
	}
}

// TestRecordRefreshesKeepsALaterFailure keeps a failure another command recorded after the
// refresh read the record, whether the record held the revocation then or not: the refresh
// tried what the node had been handed before, not what it was handed since.
func TestRecordRefreshesKeepsALaterFailure(t *testing.T) {
	later := []Unrefreshed{
		{Layer: "kubernetes", Node: "w1", Recorded: laterAt},
		{Layer: "kubernetes", Node: "w2", Listed: true, Recorded: laterAt},
	}
	s := Secrets{Unrefreshed: slices.Clone(later)}
	// w1 was recorded when the refresh read the record, w2 was not.
	if s.RecordRefreshes([]Unrefreshed{{Layer: "kubernetes", Node: "w1", Recorded: readAt}, {Layer: "kubernetes", Node: "w2"}}, nil, recordAt) {
		t.Errorf("recorded %v, want %v kept", s.Unrefreshed, later)
	}
}

// TestRecordRefreshesKeepsListed keeps a revocation marked listed when a refresh that failed
// records it again without the mark.
func TestRecordRefreshesKeepsListed(t *testing.T) {
	s := Secrets{Unrefreshed: []Unrefreshed{{Layer: "kubernetes", Node: "w1", Listed: true, Recorded: laterAt}}}
	s.RecordRefreshes([]Unrefreshed{{Layer: "kubernetes", Node: "w1"}}, []Unrefreshed{{Layer: "kubernetes", Node: "w1"}}, recordAt)
	if want := []Unrefreshed{{Layer: "kubernetes", Node: "w1", Listed: true, Recorded: recordAt}}; !slices.Equal(s.Unrefreshed, want) {
		t.Errorf("recorded %v, want %v", s.Unrefreshed, want)
	}
}

// TestUnrefreshedRoundTrip keeps the record in the secrets file, under camelCase names, and leaves
// it out while it is empty.
func TestUnrefreshedRoundTrip(t *testing.T) {
	s := generate(t)
	data, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "unrefreshedRevocations") {
		t.Error("an empty record is written")
	}
	s.Unrefreshed = []Unrefreshed{{Layer: "kubernetes", Node: "w1", Listed: true, Recorded: readAt}}
	if data, err = s.Encode(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"unrefreshedRevocations": [`) || !strings.Contains(string(data), `"listed": true`) ||
		!strings.Contains(string(data), `"recorded": "2026-11-01T08:00:00Z"`) {
		t.Errorf("the record is not written as unrefreshedRevocations:\n%s", data)
	}
	read, err := Read(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(read.Unrefreshed, s.Unrefreshed) {
		t.Errorf("read back %v, want %v", read.Unrefreshed, s.Unrefreshed)
	}
}

func TestUnrefreshedOf(t *testing.T) {
	s := Secrets{Unrefreshed: []Unrefreshed{{Layer: "kubernetes", Node: "w1"}, {Layer: "kubernetes", Node: "w2"}, {Layer: "router", Node: "r1"}}}
	if got := s.UnrefreshedOf("kubernetes"); !slices.Equal(got, []string{"w1", "w2"}) {
		t.Errorf("kubernetes: %v, want w1 and w2", got)
	}
	if got := s.UnrefreshedOf("other"); got != nil {
		t.Errorf("other: %v, want none", got)
	}
}
