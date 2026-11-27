package clustersecrets

import (
	"slices"
	"strings"
	"testing"
)

// TestRecordRefreshes drops the revocations tried that are not left, lists those left once, and
// keeps the others, which another command recorded.
func TestRecordRefreshes(t *testing.T) {
	s := Secrets{Unrefreshed: []Unrefreshed{
		{Layer: "kubernetes", Node: "w1", Listed: true},
		{Layer: "kubernetes", Node: "w2"},
		{Layer: "other", Node: "w1"},
	}}
	before := slices.Clone(s.Unrefreshed)
	changed := s.RecordRefreshes(
		[]Unrefreshed{{Layer: "kubernetes", Node: "w1"}, {Layer: "kubernetes", Node: "w2"}, {Layer: "kubernetes", Node: "w3"}},
		[]Unrefreshed{{Layer: "kubernetes", Node: "w2", Listed: true}, {Layer: "kubernetes", Node: "w3"}},
	)
	want := []Unrefreshed{
		{Layer: "kubernetes", Node: "w2", Listed: true},
		{Layer: "kubernetes", Node: "w3"},
		{Layer: "other", Node: "w1"},
	}
	if !changed || !slices.Equal(s.Unrefreshed, want) {
		t.Errorf("changed %v, recorded %v; want true and %v", changed, s.Unrefreshed, want)
	}
	if before[0].Node != "w1" || !before[0].Listed {
		t.Error("RecordRefreshes changed the slice it replaced")
	}
	if s.RecordRefreshes(nil, nil) {
		t.Error("recording nothing changed the record")
	}
	s.RecordRefreshes(want, nil)
	if s.Unrefreshed != nil {
		t.Errorf("recorded %v, want none", s.Unrefreshed)
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
	s.Unrefreshed = []Unrefreshed{{Layer: "kubernetes", Node: "w1", Listed: true}}
	if data, err = s.Encode(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"unrefreshedRevocations": [`) || !strings.Contains(string(data), `"listed": true`) {
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
