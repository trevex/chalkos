package ociimage_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/image/imagetest"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/ociimage/ocitest"
)

// writeImage writes the image directory of a test image with its contract into dir and returns
// the root hash of its store.
func writeImage(t *testing.T, dir string) []byte {
	t.Helper()
	img := imagetest.Image{Variant: "node", Platform: "metal", Seed: "1"}
	root := imagetest.Write(t, dir, img)
	imagetest.WriteContract(t, dir, img, root)
	return root
}

// writeLayout writes the layout of the image directory in to out and returns its manifest's
// digest.
func writeLayout(t *testing.T, in, out string) v1.Hash {
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

// smallBlobs are the five blobs of a small test image's layers, a few bytes each, built fresh so
// a test may replace one before calling ociimage.Build.
func smallBlobs(t *testing.T) (store, hashTree, uki, bootLoader, contract ociimage.Blob) {
	t.Helper()
	c := ocitest.Contract()
	tar, err := c.Tar()
	if err != nil {
		t.Fatal(err)
	}
	return ociimage.BytesBlob(ociimage.StoreType, []byte("store")),
		ociimage.BytesBlob(ociimage.HashTreeType, []byte("hash tree")),
		ociimage.BytesBlob(ociimage.UKIType, []byte("uki")),
		ociimage.BytesBlob(ociimage.BootLoaderType, []byte("boot loader")),
		ociimage.BytesBlob(ociimage.ContractType, tar)
}

// TestWriteLayout writes the layout of an image directory: one manifest of the chalkos artifact
// type, whose header describes the image and whose layers are the store data its root hash
// covers, its hash tree, UKI and boot loader, and its contract.
func TestWriteLayout(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in")
	root := writeImage(t, in)
	out := filepath.Join(dir, "out")
	d := writeLayout(t, in, out)
	img, err := ociimage.ReadLayout(out)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := img.Digest(); err != nil || got != d {
		t.Errorf("the layout's manifest is %s, %v; WriteLayout returned %s", got, err, d)
	}
	h, err := ociimage.ReadHeader(img)
	want := ociimage.Header{ImageID: "chalkos", RootHash: hex.EncodeToString(root), Architecture: "x86-64", Platform: "metal", Revision: "abc123", Tag: "v1"}
	if err != nil || h != want {
		t.Errorf("header = %+v, %v; want %+v", h, err, want)
	}
	m, err := img.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	var got []types.MediaType
	for _, l := range m.Layers {
		got = append(got, l.MediaType)
	}
	wantTypes := []types.MediaType{ociimage.StoreType, ociimage.HashTreeType, ociimage.UKIType, ociimage.BootLoaderType, ociimage.ContractType}
	if m.ArtifactType != ociimage.ArtifactType || m.Config.MediaType != ociimage.HeaderType || !slices.Equal(got, wantTypes) {
		t.Errorf("manifest = %s, %s, %v", m.ArtifactType, m.Config.MediaType, got)
	}
	parts, err := image.ReadPartitions(filepath.Join(in, "repart-output.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.Open(filepath.Join(in, "chalkos.raw"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	store, err := image.ReadStore(raw, parts)
	if err != nil {
		t.Fatal(err)
	}
	if want, _, _ := v1.SHA256(io.NewSectionReader(store.Data, 0, store.Data.Size())); m.Layers[0].Digest != want {
		t.Errorf("the store layer is %s, not the store data's %s", m.Layers[0].Digest, want)
	}
	c, err := ociimage.ReadContract(img)
	wantContract, _ := ociimage.ReadContractDir(in)
	if err != nil || !reflect.DeepEqual(c, wantContract) {
		t.Errorf("contract = %+v, %v; want %+v", c, err, wantContract)
	}
}

// TestWriteLayoutIsReproducible writes the same layout of an image directory twice, as a Nix
// build of it must.
func TestWriteLayoutIsReproducible(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in")
	writeImage(t, in)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if da, db := writeLayout(t, in, a), writeLayout(t, in, b); da != db {
		t.Errorf("manifests %s and %s differ", da, db)
	}
	for _, f := range []string{"index.json", "oci-layout"} {
		x, _ := os.ReadFile(filepath.Join(a, f))
		y, _ := os.ReadFile(filepath.Join(b, f))
		if !bytes.Equal(x, y) {
			t.Errorf("%s differs: %s and %s", f, x, y)
		}
	}
}

// TestWriteLayoutNeedsANewDirectory refuses an output that exists, or whose parent does not, and
// leaves nothing behind.
func TestWriteLayoutNeedsANewDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "exists"), 0o755); err != nil {
		t.Fatal(err)
	}
	for out, want := range map[string]string{
		filepath.Join(dir, "exists"):         "exists",
		filepath.Join(dir, "missing", "out"): "no such file or directory",
	} {
		if _, err := ociimage.WriteLayout(context.Background(), ocitest.Image(t, ocitest.Header(), ocitest.Contract()), out); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("WriteLayout(%s) = %v, want %q", out, err, want)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("the directory holds %v, want exists alone", entries)
	}
}

// TestContractTar writes a contract as a tar of regular files in name order, owned by 0:0 and
// modified at the epoch, and reads it back.
func TestContractTar(t *testing.T) {
	c := ociimage.Contract{
		Metadata:    []byte(`{"schemaVersion": 0}`),
		Schemas:     map[string][]byte{"b.json": []byte("b"), "a.json": []byte("a")},
		Definitions: map[string][]byte{"00-esp.conf": []byte("esp")},
		Facts:       map[string][]byte{"kubernetes": []byte("{}")},
	}
	data, err := c.Tar()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if h.Typeflag != tar.TypeReg || h.Mode != 0o644 || h.Uid != 0 || h.Gid != 0 || !h.ModTime.Equal(time.Unix(0, 0)) {
			t.Errorf("%s: %+v", h.Name, h)
		}
	}
	if want := []string{"facts/kubernetes.json", "metadata.json", "repart.d/00-esp.conf", "schemas/a.json", "schemas/b.json"}; !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	if got, err := ociimage.ParseContract(bytes.NewReader(data)); err != nil || !reflect.DeepEqual(got, c) {
		t.Errorf("ParseContract = %+v, %v", got, err)
	}
}

// TestContractReadFile reads a layer's facts by their name in the image's directory, and nothing
// for a layer without facts or for another name.
func TestContractReadFile(t *testing.T) {
	c := ociimage.Contract{Metadata: []byte("{}"), Facts: map[string][]byte{"kubernetes": []byte(`{"version": "1.37.1"}`)}}
	if data, err := c.ReadFile("facts/kubernetes.json"); err != nil || string(data) != `{"version": "1.37.1"}` {
		t.Errorf("facts/kubernetes.json = %q, %v", data, err)
	}
	for _, name := range []string{"metadata.json", "facts/router.json", "kubernetes.json", "facts/kubernetes"} {
		if _, err := c.ReadFile(name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: %v, want fs.ErrNotExist", name, err)
		}
	}
	if _, err := (ociimage.Contract{}).ReadFile("facts/kubernetes.json"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the facts of an image without any: %v, want fs.ErrNotExist", err)
	}
}

// TestParseContract skips directories and files outside the contract, and refuses a link and a
// contract without metadata.json.
func TestParseContract(t *testing.T) {
	tarOf := func(entries ...*tar.Header) []byte {
		var buf bytes.Buffer
		w := tar.NewWriter(&buf)
		for _, h := range entries {
			if err := w.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			w.Write(bytes.Repeat([]byte("x"), int(h.Size)))
		}
		w.Close()
		return buf.Bytes()
	}
	reg := func(name string) *tar.Header {
		return &tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: 1}
	}
	valid := []*tar.Header{reg("metadata.json"), reg("schemas/a.json"), reg("repart.d/00-esp.conf")}
	c, err := ociimage.ParseContract(bytes.NewReader(tarOf(append(valid, &tar.Header{Typeflag: tar.TypeDir, Name: "notes/", Mode: 0o755}, reg("notes/later.txt"))...)))
	if err != nil || len(c.Schemas) != 1 || len(c.Definitions) != 1 {
		t.Errorf("ParseContract = %+v, %v", c, err)
	}
	for name, entries := range map[string][]*tar.Header{
		"is no regular file":                                       append(valid, &tar.Header{Typeflag: tar.TypeSymlink, Name: "schemas/b.json", Linkname: "/etc/passwd"}),
		"the contract has no metadata":                             valid[1:],
		"holds metadata.json twice":                                append(valid, reg("metadata.json")),
		"holds facts/test.json twice":                              append(valid, reg("facts/test.json"), reg("facts/test.json")),
		`"facts/.json", the facts of a layer whose name is not`:    append(valid, reg("facts/.json")),
		`"facts/Kubernetes.json", the facts of a layer whose name`: append(valid, reg("facts/Kubernetes.json")),
	} {
		if _, err := ociimage.ParseContract(bytes.NewReader(tarOf(entries...))); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("ParseContract = %v, want %q", err, name)
		}
	}
}

// TestReadContractDirRefusesFactsWithoutALayerName refuses an image directory whose facts/ holds
// .json, which names no layer.
func TestReadContractDirRefusesFactsWithoutALayerName(t *testing.T) {
	dir := t.TempDir()
	writeImage(t, dir)
	if _, err := ociimage.ReadContractDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "facts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "facts", ".json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ociimage.ReadContractDir(dir); err == nil || !strings.Contains(err.Error(), `"facts/.json", the facts of a layer whose name`) {
		t.Errorf("ReadContractDir = %v, want the facts of a layer without a name refused", err)
	}
}

// TestFromDirectoryRefuses refuses an image directory whose metadata.json names another root hash
// or platform than its raw image, and one whose UKI names no variant, such as the installer's.
func TestFromDirectoryRefuses(t *testing.T) {
	metal := imagetest.Image{Variant: "node", Platform: "metal", Seed: "1"}
	noVariant := imagetest.Image{Platform: "metal", Seed: "1"}
	for want, tc := range map[string]struct {
		img, contract imagetest.Image
		root          []byte
	}{
		`metadata.json names the root hash "cdcd`:                                   {metal, metal, bytes.Repeat([]byte{0xcd}, 32)},
		`metadata.json names the platform "kvm", but the image's UKI names "metal"`: {metal, imagetest.Image{Variant: "node", Platform: "kvm"}, nil},
		"names no variant": {noVariant, noVariant, nil},
	} {
		in := t.TempDir()
		root := imagetest.Write(t, in, tc.img)
		if tc.root != nil {
			root = tc.root
		}
		imagetest.WriteContract(t, in, tc.contract, root)
		if _, _, err := ociimage.FromDirectory(context.Background(), in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("FromDirectory = %v, want %q", err, want)
		}
	}
}

// TestWriteLayoutStopsOnCtxCancellation refuses to write a layout once ctx has already ended, and
// leaves nothing behind.
func TestWriteLayoutStopsOnCtxCancellation(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ociimage.WriteLayout(ctx, ocitest.Image(t, ocitest.Header(), ocitest.Contract()), out); err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("WriteLayout = %v, want context.Canceled", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the directory holds %v, want nothing left behind", entries)
	}
}

// TestWriteLayoutRefusesALayerWhoseContentDoesNotMatchItsDigest fails, and leaves nothing behind,
// when a blob's Open does not return what its Digest and Size were computed from.
func TestWriteLayoutRefusesALayerWhoseContentDoesNotMatchItsDigest(t *testing.T) {
	store, hashTree, uki, bootLoader, contract := smallBlobs(t)
	store.Open = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader([]byte("STORE"))), nil }
	img, err := ociimage.Build(ocitest.Header(), store, hashTree, uki, bootLoader, contract)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	if _, err := ociimage.WriteLayout(context.Background(), img, out); err == nil {
		t.Error("WriteLayout = nil error, want a digest mismatch")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the directory holds %v, want nothing left behind", entries)
	}
}

// TestReadLayoutRefusesACorruptManifestOrConfig refuses a layout whose manifest or config blob does
// not hash to the digest that names it.
func TestReadLayoutRefusesACorruptManifestOrConfig(t *testing.T) {
	for _, which := range []string{"manifest", "config"} {
		t.Run(which, func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "in")
			writeImage(t, in)
			out := filepath.Join(dir, "out")
			target := writeLayout(t, in, out)
			if which == "config" {
				img, err := ociimage.ReadLayout(out)
				if err != nil {
					t.Fatal(err)
				}
				m, err := img.Manifest()
				if err != nil {
					t.Fatal(err)
				}
				target = m.Config.Digest
			}
			path := filepath.Join(out, "blobs", target.Algorithm, target.Hex)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data[len(data)-1] ^= 1
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ociimage.ReadLayout(out); err == nil {
				t.Errorf("ReadLayout with a corrupt %s = nil error", which)
			}
		})
	}
}

// TestReadContractRefusesATamperedBlob serves a chalkos image from an in-memory registry through
// a proxy that flips the contract layer's last byte, and checks that ReadContract notices:
// go-containerregistry verifies a remote blob's digest only once a reader of it reaches end of
// stream, which archive/tar's reader does not reach on its own.
func TestReadContractRefusesATamperedBlob(t *testing.T) {
	img := ocitest.Image(t, ocitest.Header(), ocitest.Contract())
	l, err := ociimage.Layer(img, ociimage.ContractType)
	if err != nil {
		t.Fatal(err)
	}
	contractDigest, err := l.Digest()
	if err != nil {
		t.Fatal(err)
	}
	blob, err := l.Compressed()
	if err != nil {
		t.Fatal(err)
	}
	tampered, err := io.ReadAll(blob)
	if err != nil {
		t.Fatal(err)
	}
	tampered[len(tampered)-1] ^= 1
	host := ocitest.RegistryWith(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/blobs/"+contractDigest.String()) {
			w.Write(tampered)
			return true
		}
		return false
	})
	ref, err := name.ParseReference(host + "/x:v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	ri, err := remote.Image(ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ociimage.ReadContract(ri); err == nil {
		t.Error("ReadContract over a tampered blob = nil error, want a digest mismatch")
	}
}

// TestReadContractRefusesAnOversizedLayer refuses a contract layer larger than a contract may be,
// by its descriptor's size alone, before reading it.
func TestReadContractRefusesAnOversizedLayer(t *testing.T) {
	store, hashTree, uki, bootLoader, _ := smallBlobs(t)
	big := ociimage.BytesBlob(ociimage.ContractType, bytes.Repeat([]byte("x"), 16<<20+1))
	img, err := ociimage.Build(ocitest.Header(), store, hashTree, uki, bootLoader, big)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ociimage.ReadContract(img); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("ReadContract = %v", err)
	}
}

// TestReadHeaderRefusesANonChalkosImage refuses a manifest of another artifact type.
func TestReadHeaderRefusesANonChalkosImage(t *testing.T) {
	img, err := random.Image(1024, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ociimage.ReadHeader(img); err == nil || !strings.Contains(err.Error(), "it is no chalkos image") {
		t.Errorf("ReadHeader = %v", err)
	}
}

// TestReplaceRefusesExtraLayers refuses a manifest with a layer besides the five known ones.
func TestReplaceRefusesExtraLayers(t *testing.T) {
	img, err := mutate.AppendLayers(ocitest.Image(t, ocitest.Header(), ocitest.Contract()), static.NewLayer([]byte("extra"), "application/vnd.chalkos.image.extra.v1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ociimage.Replace(img); err == nil || !strings.Contains(err.Error(), "layers") {
		t.Errorf("Replace = %v", err)
	}
}

// TestBuildRefusesDuplicateLayerDigests refuses two layers of the same digest, even of different
// media types.
func TestBuildRefusesDuplicateLayerDigests(t *testing.T) {
	store, _, uki, bootLoader, contract := smallBlobs(t)
	dup := ociimage.BytesBlob(ociimage.HashTreeType, []byte("store"))
	if _, err := ociimage.Build(ocitest.Header(), store, dup, uki, bootLoader, contract); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Errorf("Build = %v", err)
	}
}
