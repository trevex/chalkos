package constants

import "testing"

// constants.json decodes with every value set; lib/kinds/api.nix reads the same file, as the
// eval-kinds check's testSharedConstants holds.
func TestConstants(t *testing.T) {
	if Version == "" || len(Groups) == 0 || len(NetworkdKinds) == 0 || len(Normalisers) == 0 {
		t.Fatalf("constants.json: %+v", shared)
	}
	if got := APIVersion("core"); got != "chalkos.dev/v1alpha1" {
		t.Errorf("APIVersion(core) = %q", got)
	}
	for name, want := range map[string]bool{"a": true, "bird-conf": true, "a.b": false, "-a": false, "A": false, "": false} {
		if PlainName.MatchString(name) != want {
			t.Errorf("PlainName matches %q: %v, want %v", name, !want, want)
		}
	}
	for name, want := range map[string]bool{"a.b": true, "a-b.c": true, "a.": false, "A": false, "a b": false} {
		if DocumentName.MatchString(name) != want {
			t.Errorf("DocumentName matches %q: %v, want %v", name, !want, want)
		}
	}
}
