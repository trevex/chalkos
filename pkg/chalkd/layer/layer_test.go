package layer

import "testing"

// A permission that says nothing of the console refuses the procedure to it.
func TestConsoleDeniedByDefault(t *testing.T) {
	if got := (Permission{}).Console; got != ConsoleDenied {
		t.Errorf("a permission without Console gives the console %v", got)
	}
}
