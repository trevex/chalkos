package clustersecrets

import (
	"slices"
	"strings"
	"time"
)

// Unrefreshed is the revocation of a node, which a layer lists as revoked, that a command run with
// --ignore-revoked did not refresh after it handed the node certificates or CA material. Until a
// refresh succeeds, pruning the layer's revoked nodes may drop the node while what it got is still
// valid, so chalkctl prunes nothing for the layer and retries the refresh first.
type Unrefreshed struct {
	// Layer is the name of the layer that lists the node as revoked.
	Layer string `json:"layer"`
	Node  string `json:"node"`
	// Listed is set when the layer listed the node as revoked before it was handed what it got,
	// so a refresh that finds it dropped from the list since revokes it again. Otherwise whether
	// it was revoked was unknown, and a refresh first reads whether it is.
	Listed bool `json:"listed,omitempty"`
	// Recorded is when the failed refresh was recorded. A refresh that read the record before
	// another command recorded the revocation again clears only what it read, which the time
	// tells apart.
	Recorded time.Time `json:"recorded,omitzero"`
}

// RecordRefreshes updates Unrefreshed after a refresh of the revocations tried, of which those in
// left were not refreshed. Each of tried carries the time the record held it with when the
// refresh read the record, the zero time if it held none. RecordRefreshes drops an entry of
// tried unless it is left or another command recorded it again since, at another time; it lists
// each of left once, recorded at now, in place of an entry for the same layer and node, and
// listed if either is; it keeps every other entry. It reports whether the record changed. The
// slice it replaces is left as it was.
func (s *Secrets) RecordRefreshes(tried, left []Unrefreshed, now time.Time) bool {
	same := func(a Unrefreshed) func(Unrefreshed) bool {
		return func(b Unrefreshed) bool { return a.Layer == b.Layer && a.Node == b.Node }
	}
	left = slices.Clone(left)
	var record []Unrefreshed
	for _, u := range s.Unrefreshed {
		if i := slices.IndexFunc(left, same(u)); i >= 0 {
			// Left again: recorded anew below.
			left[i].Listed = left[i].Listed || u.Listed
			continue
		}
		if !slices.ContainsFunc(tried, func(t Unrefreshed) bool { return same(u)(t) && t.Recorded.Equal(u.Recorded) }) {
			record = append(record, u)
		}
	}
	for _, u := range left {
		if !slices.ContainsFunc(record, same(u)) {
			u.Recorded = now.UTC()
			record = append(record, u)
		}
	}
	slices.SortFunc(record, func(a, b Unrefreshed) int {
		if a.Layer != b.Layer {
			return strings.Compare(a.Layer, b.Layer)
		}
		return strings.Compare(a.Node, b.Node)
	})
	if slices.EqualFunc(record, s.Unrefreshed, func(a, b Unrefreshed) bool {
		return same(a)(b) && a.Listed == b.Listed && a.Recorded.Equal(b.Recorded)
	}) {
		return false
	}
	s.Unrefreshed = record
	return true
}

// UnrefreshedOf are the nodes whose revocations by the layer called layer Unrefreshed records, in
// name order.
func (s Secrets) UnrefreshedOf(layer string) []string {
	var names []string
	for _, u := range s.Unrefreshed {
		if u.Layer == layer {
			names = append(names, u.Node)
		}
	}
	return names
}
