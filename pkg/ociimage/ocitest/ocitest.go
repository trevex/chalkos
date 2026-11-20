// Package ocitest serves OCI registries from memory and builds small chalkos images for tests.
package ocitest

import (
	"context"
	"encoding/base64"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/trevex/chalkos/pkg/ociimage"
)

// Registry serves an empty registry from memory until the test ends and returns its host,
// 127.0.0.1:<port>, which go-containerregistry reaches over plain HTTP as a loopback address.
func Registry(t testing.TB) string {
	return RegistryWith(t, nil)
}

// RegistryWith is Registry that hands each request to intercept first and serves it unless
// intercept handled it and returns true. A nil intercept handles none.
func RegistryWith(t testing.TB, intercept func(w http.ResponseWriter, r *http.Request) bool) string {
	t.Helper()
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if intercept == nil || !intercept(w, r) {
			reg.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// CountingRegistry is Registry that counts the manifest requests it answers.
func CountingRegistry(t testing.TB) (string, *atomic.Int64) {
	t.Helper()
	var manifests atomic.Int64
	host := RegistryWith(t, func(_ http.ResponseWriter, r *http.Request) bool {
		if strings.Contains(r.URL.Path, "/manifests/") {
			manifests.Add(1)
		}
		return false
	})
	return host, &manifests
}

// ServedBlobs is Registry that records the digest of each blob it serves.
func ServedBlobs(t testing.TB) (string, *sync.Map) {
	t.Helper()
	var served sync.Map
	host := RegistryWith(t, func(_ http.ResponseWriter, r *http.Request) bool {
		if _, digest, ok := strings.Cut(r.URL.Path, "/blobs/"); ok && r.Method == http.MethodGet {
			served.Store(digest, true)
		}
		return false
	})
	return host, &served
}

// Header is the header of a small test image, built fresh so a test may change its copy.
func Header() ociimage.Header {
	return ociimage.Header{ImageID: "chalkos", RootHash: strings.Repeat("ab", 32), Architecture: "x86-64", Platform: "metal"}
}

// Contract is a small contract, built fresh so a test may change its copy.
func Contract() ociimage.Contract {
	return ociimage.Contract{Metadata: []byte("{}"), Schemas: map[string][]byte{"a.json": []byte("{}")}, Definitions: map[string][]byte{"00-esp.conf": []byte("esp")}}
}

// Image is a chalkos image of the header and the contract whose store, hash tree, UKI and boot
// loader are a few bytes each.
func Image(t testing.TB, h ociimage.Header, c ociimage.Contract) v1.Image {
	t.Helper()
	contract, err := c.Tar()
	if err != nil {
		t.Fatal(err)
	}
	img, err := ociimage.Build(h, nil,
		ociimage.BytesBlob(ociimage.StoreType, []byte("store")),
		ociimage.BytesBlob(ociimage.HashTreeType, []byte("hash tree")),
		ociimage.BytesBlob(ociimage.UKIType, []byte("uki")),
		ociimage.BytesBlob(ociimage.BootLoaderType, []byte("boot loader")),
		ociimage.BytesBlob(ociimage.ContractType, contract))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// Layout writes the OCI layout of the image directory in to out, as chalkbuild oci does, and
// returns its manifest's digest.
func Layout(t testing.TB, in, out string) v1.Hash {
	t.Helper()
	img, closeImage, err := ociimage.FromDirectory(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	defer closeImage()
	d, err := ociimage.WriteLayout(context.Background(), img, out)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// PushDirectory pushes the image of the image directory dir to ref, as chalkbuild oci and push
// do, and returns the reference of its manifest by digest.
func PushDirectory(t testing.TB, dir, ref string) string {
	t.Helper()
	img, closeImage, err := ociimage.FromDirectory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeImage()
	return Push(t, img, ref)
}

// Push pushes img to ref and returns the reference of its manifest by digest.
func Push(t testing.TB, img v1.Image, ref string) string {
	t.Helper()
	pinned, err := ociimage.Push(context.Background(), img, ref)
	if err != nil {
		t.Fatal(err)
	}
	return pinned
}

// AuthRegistry is Registry that answers only requests with the user and the password, as a
// private registry does, and asks for them with a Basic challenge otherwise. It gives the test a
// docker config holding them, through DOCKER_CONFIG, so that Push and Read authenticate with the
// default keychain as chalkbuild and chalkctl do; the test must not run in parallel.
func AuthRegistry(t testing.TB, user, password string) string {
	t.Helper()
	host := RegistryWith(t, func(w http.ResponseWriter, r *http.Request) bool {
		if u, p, ok := r.BasicAuth(); ok && u == user && p == password {
			return false
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="ocitest"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"errors": [{"code": "UNAUTHORIZED", "message": "authentication required"}]}`)
		return true
	})
	dir := t.TempDir()
	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"auths": {"`+host+`": {"auth": "`+auth+`"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", dir)
	return host
}
