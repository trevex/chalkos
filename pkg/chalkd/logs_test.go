package chalkd

import (
	"slices"
	"testing"
)

// Zero lines starts from the whole boot, also when following, which journalctl otherwise starts
// from its last ten entries.
func TestJournalArgs(t *testing.T) {
	base := []string{"--boot", "--no-pager", "--output=short-iso"}
	for _, tc := range []struct {
		unit   string
		follow bool
		lines  uint32
		want   []string
	}{
		{"", false, 0, base},
		{"u", false, 5, append(slices.Clone(base), "--lines=5", "--unit=u")},
		{"", true, 0, append(slices.Clone(base), "--no-tail", "--follow")},
		{"", true, 100, append(slices.Clone(base), "--lines=100", "--follow")},
	} {
		if got := journalArgs(tc.unit, tc.follow, tc.lines); !slices.Equal(got, tc.want) {
			t.Errorf("journalArgs(%q, %v, %d) = %q, want %q", tc.unit, tc.follow, tc.lines, got, tc.want)
		}
	}
}
