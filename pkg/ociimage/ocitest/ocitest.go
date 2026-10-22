// Package ocitest serves OCI registries from memory and builds small chalkos images for tests.
package ocitest

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/trevex/chalkos/pkg/ociimage"
)

// Registry serves an empty registry from memory until the test ends and returns its host,
// 127.0.0.1:<port>, which go-containerregistry reaches over plain HTTP as a loopback address.
func Registry(t testing.TB) string {
	host, _ := CountingRegistry(t)
	return host
}

// CountingRegistry is Registry that counts the manifest requests it answers.
func CountingRegistry(t testing.TB) (string, *atomic.Int64) {
	t.Helper()
	var manifests atomic.Int64
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/manifests/") {
			manifests.Add(1)
		}
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), &manifests
}

// Image is a chalkos image of the header and the contract whose store, hash tree, UKI and boot
// loader are a few bytes each.
func Image(t testing.TB, h ociimage.Header, c ociimage.Contract) v1.Image {
	t.Helper()
	contract, err := c.Tar()
	if err != nil {
		t.Fatal(err)
	}
	img, err := ociimage.Build(h,
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

// Push pushes img to ref and returns the reference of its manifest by digest.
func Push(t testing.TB, img v1.Image, ref string) string {
	t.Helper()
	pinned, err := ociimage.Push(context.Background(), img, ref)
	if err != nil {
		t.Fatal(err)
	}
	return pinned
}
