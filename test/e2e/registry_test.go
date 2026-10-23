package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
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

// registryGuest is where a node reaches a registry on the host, as the Kubernetes tests' nodes
// reach theirs.
const registryGuest = "10.0.2.100:5000"

// imageRegistry is a registry on the host that keeps its blobs in a directory and answers only
// requests with the user e2e and its password, as a private registry does.
type imageRegistry struct {
	host, password string
	blobs          registry.BlobPutHandler
	// served holds the digests of the blobs it served.
	served sync.Map
}

// startImageRegistry starts an imageRegistry whose blobs live in dir, which it creates.
func startImageRegistry(t *testing.T, dir, password string) *imageRegistry {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	blobs := registry.NewDiskBlobHandler(dir)
	r := &imageRegistry{password: password, blobs: blobs.(registry.BlobPutHandler)}
	reg := registry.New(registry.WithBlobHandler(blobs), registry.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if user, pass, ok := req.BasicAuth(); !ok || user != "e2e" || pass != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="e2e"`)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"errors": [{"code": "UNAUTHORIZED", "message": "authentication required"}]}`)
			return
		}
		if _, digest, ok := strings.Cut(req.URL.Path, "/blobs/"); ok && req.Method == http.MethodGet {
			r.served.Store(digest, true)
		}
		reg.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	r.host = strings.TrimPrefix(srv.URL, "http://")
	return r
}

// push writes img's blobs into the registry's directory instead of uploading them, as the registry
// would hold an upload in memory whole, and puts its manifest as repo:tag.
func (r *imageRegistry) push(t *testing.T, img v1.Image, repo, tag string) {
	t.Helper()
	ctx := context.Background()
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range layers {
		d, err := l.Digest()
		if err != nil {
			t.Fatal(err)
		}
		rc, err := l.Compressed()
		if err != nil {
			t.Fatal(err)
		}
		err = r.blobs.Put(ctx, repo, d, rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	config, err := img.RawConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cd, err := img.ConfigName()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.blobs.Put(ctx, repo, cd, io.NopCloser(bytes.NewReader(config))); err != nil {
		t.Fatal(err)
	}
	ref, err := name.NewTag(r.host + "/" + repo + ":" + tag)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Put(ref, img, remote.WithAuth(&authn.Basic{Username: "e2e", Password: r.password})); err != nil {
		t.Fatal(err)
	}
}
