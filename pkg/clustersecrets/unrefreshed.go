package clustersecrets

import (
	"slices"
	"strings"
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
}

// RecordRefreshes updates Unrefreshed after a refresh of the revocations tried, of which those in
// left were not refreshed: it drops the others of tried, lists each of left once, in place of an
// entry for the same layer and node, and keeps every other entry. It reports whether the record
// changed. The slice it replaces is left as it was.
func (s *Secrets) RecordRefreshes(tried, left []Unrefreshed) bool {
	same := func(a Unrefreshed) func(Unrefreshed) bool {
		return func(b Unrefreshed) bool { return a.Layer == b.Layer && a.Node == b.Node }
	}
	var record []Unrefreshed
	for _, u := range s.Unrefreshed {
		if !slices.ContainsFunc(tried, same(u)) && !slices.ContainsFunc(left, same(u)) {
			record = append(record, u)
		}
	}
	for _, u := range left {
		if !slices.ContainsFunc(record, same(u)) {
			record = append(record, u)
		}
	}
	slices.SortFunc(record, func(a, b Unrefreshed) int {
		if a.Layer != b.Layer {
			return strings.Compare(a.Layer, b.Layer)
		}
		return strings.Compare(a.Node, b.Node)
	})
	if slices.Equal(record, s.Unrefreshed) {
		return false
	}
	s.Unrefreshed = record
	return true
}
