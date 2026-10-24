package withheld

import "testing"

func TestKind(t *testing.T) {
	for kind, want := range map[string]bool{"Secret": true, "Addon": true, "Network": false, "secret": false, "": false} {
		if got := Kind(kind); got != want {
			t.Errorf("Kind(%q) = %v, want %v", kind, got, want)
		}
	}
}
