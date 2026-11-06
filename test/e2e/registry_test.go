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
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// The private repository of startRegistry's registry, which answers only the user e2e with
// privatePassword. The chalklab cluster's nodes reach it as privateImage through a Mirror of
// private.chalkos.test, whose Registry in groups/k8s.yaml holds the password.
const (
	privateRepo     = "e2e/busybox"
	privatePassword = "pull-s3cret"
	privateImage    = "private.chalkos.test/" + privateRepo + ":1.37.0"
)

// startRegistry serves the images CHALKLAB_K8S_IMAGES lists from memory, and busybox again as
// privateRepo, which asks for credentials, and returns the host address it listens on. Each
// image is served under its repository without the registry's host, as containerd asks a mirror
// for it, and under the digest of the manifest upstream serves, which a pinned reference names.
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
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/v2/"+privateRepo+"/") {
			if user, pass, ok := req.BasicAuth(); !ok || user != "e2e" || pass != privatePassword {
				unauthorized(w)
				return
			}
		}
		reg.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	for ref, dir := range images {
		tagged, digest, _ := strings.Cut(ref, "@")
		tag, err := name.NewTag(tagged)
		if err != nil {
			t.Fatal(err)
		}
		pushLayout(t, dir, host+"/"+tag.RepositoryStr()+":"+tag.TagStr(), digest)
	}
	busybox, ok := images["docker.io/library/busybox:1.37.0"]
	if !ok {
		t.Fatal("CHALKLAB_K8S_IMAGES holds no busybox, which the private repository serves")
	}
	pushLayout(t, busybox, host+"/"+privateRepo+":1.37.0", "", remote.WithAuth(&authn.Basic{Username: "e2e", Password: privatePassword}))
	return host
}

// pushLayout pushes the one image of the OCI layout dir to ref with its manifest's bytes, and
// fails the test unless its digest is the digest given, when one is.
func pushLayout(t *testing.T, dir, ref, digest string, options ...remote.Option) {
	t.Helper()
	index, err := layout.ImageIndexFromPath(dir)
	if err != nil {
		t.Fatalf("%s: %v", dir, err)
	}
	manifest, err := index.IndexManifest()
	if err != nil {
		t.Fatalf("%s: %v", dir, err)
	}
	if len(manifest.Manifests) != 1 {
		t.Fatalf("%s holds %d images, want one", dir, len(manifest.Manifests))
	}
	img, err := index.Image(manifest.Manifests[0].Digest)
	if err != nil {
		t.Fatalf("%s: %v", dir, err)
	}
	dst, err := name.NewTag(ref, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(dst, img, options...); err != nil {
		t.Fatalf("push %s: %v", ref, err)
	}
	got, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if digest != "" && got.String() != digest {
		t.Fatalf("%s pushed as %s, but its pinned reference names %s", dir, got, digest)
	}
}

// unauthorized answers a request that lacks the right credentials as a registry with Basic
// authentication does.
func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="e2e"`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	io.WriteString(w, `{"errors": [{"code": "UNAUTHORIZED", "message": "authentication required"}]}`)
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
			unauthorized(w)
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
