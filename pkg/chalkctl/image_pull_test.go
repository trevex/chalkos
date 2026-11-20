package chalkctl

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/trevex/chalkos/pkg/image/imagetest"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/ociimage/ocitest"
)

// TestImagePull pulls an image by tag into an image directory and prints the digest it resolved
// the tag to, then again, which leaves the directory as it is and says so.
func TestImagePull(t *testing.T) {
	ta := newTestApp(t)
	built := filepath.Join(ta.dir, "built")
	imagetest.WriteDir(t, built, imagetest.Image{Name: "test-metal", Platform: "metal", Seed: "a"})
	imagetest.WriteDefinitions(t, built)
	ref := ocitest.Registry(t) + "/lab/test-metal:v1"
	pinned := ocitest.PushDirectory(t, built, ref)
	out := filepath.Join(ta.dir, "images", "pulled")

	if err := ta.run(context.Background(), []string{"image", "pull", ref, out}); err != nil {
		t.Fatal(err)
	}
	if want := "pulled " + ref + " as " + pinned + " into " + out + "\n"; ta.stdout.String() != want {
		t.Errorf("stdout = %q, want %q", ta.stdout, want)
	}
	if _, closeImage, err := ociimage.FromDirectory(context.Background(), out); err != nil {
		t.Errorf("the pulled directory: %v", err)
	} else {
		closeImage()
	}

	ta.stdout.Reset()
	if err := ta.run(context.Background(), []string{"image", "pull", pinned, out}); err != nil {
		t.Fatal(err)
	}
	if want := out + " holds " + pinned + " already; nothing pulled\n"; ta.stdout.String() != want {
		t.Errorf("stdout = %q, want %q", ta.stdout, want)
	}
}
