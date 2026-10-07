package e2e

import (
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// startRegistry serves the images CHALKLAB_K8S_IMAGES lists from memory and returns the host
// address it listens on. Each image is served under its repository without the registry's
// host, as containerd asks a mirror for it.
func startRegistry(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("CHALKLAB_K8S_IMAGES"))
	if err != nil {
		t.Fatal(err)
	}
	var images map[string]string
	if err := json.Unmarshal(data, &images); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	for ref, archive := range images {
		tag, err := name.NewTag(ref)
		if err != nil {
			t.Fatal(err)
		}
		img, err := tarball.ImageFromPath(archive, nil)
		if err != nil {
			t.Fatalf("%s: %v", archive, err)
		}
		dst, err := name.NewTag(host+"/"+tag.RepositoryStr()+":"+tag.TagStr(), name.Insecure)
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.Write(dst, img); err != nil {
			t.Fatalf("push %s: %v", ref, err)
		}
	}
	return host
}
