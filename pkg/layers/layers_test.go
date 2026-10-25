package layers

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Read names a layer by each marker in the directory, a symlink as NixOS's /etc holds it
// included; a directory is no marker.
func TestRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "router"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "kubernetes")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "kubernetes")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "facts"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Names(); !slices.Equal(got, []string{"kubernetes", "router"}) {
		t.Errorf("layers %q, want kubernetes and router", got)
	}
	if !s.Has(Kubernetes) || !s.Has(Base) || s.Has("facts") {
		t.Errorf("Has: kubernetes %v, base %v, facts %v", s.Has(Kubernetes), s.Has(Base), s.Has("facts"))
	}
}

// An image of the base alone has no marker directory.
func TestReadWithoutMarkers(t *testing.T) {
	s, err := Read(filepath.Join(t.TempDir(), "layers"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Names()) != 0 || !s.Has(Base) || s.Has(Kubernetes) {
		t.Errorf("layers %q of an image without markers", s.Names())
	}
}

func TestReadFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "layers")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(file); err == nil {
		t.Error("read the layers of a file")
	}
}

// The zero Set holds the base alone; Of holds what it names but the base, which needs no name.
func TestOf(t *testing.T) {
	var none Set
	if !none.Has(Base) || none.Has(Kubernetes) || len(none.Names()) != 0 {
		t.Errorf("the zero set has %q", none.Names())
	}
	if s := Of(Kubernetes); !s.Has(Kubernetes) || !slices.Equal(s.Names(), []string{Kubernetes}) {
		t.Errorf("Of(kubernetes) has %q", s.Names())
	}
	if s := Of(Base); !s.Has(Base) || len(s.Names()) != 0 {
		t.Errorf("Of(base) has %q", s.Names())
	}
}
