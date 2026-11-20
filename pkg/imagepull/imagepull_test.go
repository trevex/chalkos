package imagepull_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/image/imagetest"
	"github.com/trevex/chalkos/pkg/imagepull"
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/ociimage/ocitest"
	"github.com/trevex/chalkos/pkg/storage/gpt"
)

// buildImage writes an image directory, as an image's build lays it out, with the repart
// definitions of a whole system region.
func buildImage(t *testing.T, seed string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "built")
	imagetest.WriteDir(t, dir, imagetest.Image{Name: "node", Platform: "metal", Seed: seed})
	imagetest.WriteDefinitions(t, dir)
	return dir
}

// fromDirectory is the image of an image directory, with its manifest's digest.
func fromDirectory(t *testing.T, dir string) (ociimage.Header, v1.Hash) {
	t.Helper()
	img, closeImage, err := ociimage.FromDirectory(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeImage()
	h, err := ociimage.ReadHeader(img)
	if err != nil {
		t.Fatal(err)
	}
	d, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return h, d
}

// TestDirectoryRoundTrip pulls an image by tag into an image directory that reads as the image
// pushed: the same header, contract and manifest digest. The tag resolves to the digest, which
// the pull reports. Its raw disk image has the GPT, the ESP and slot A an image's build gives it,
// and its contract's files are those of the built directory.
func TestDirectoryRoundTrip(t *testing.T) {
	built := buildImage(t, "a")
	ref := ocitest.Registry(t) + "/lab/node-metal:v1"
	pinned := ocitest.PushDirectory(t, built, ref)
	out := filepath.Join(t.TempDir(), "node-metal")

	p, err := imagepull.Directory(context.Background(), ref, out)
	if err != nil {
		t.Fatal(err)
	}
	wantHeader, wantDigest := fromDirectory(t, built)
	if p.Reference != pinned || p.Header != wantHeader || p.Present {
		t.Errorf("pulled %+v, want %s of %+v", p, pinned, wantHeader)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("the pulled directory's mode is %v, want it readable to all", info.Mode())
	}
	h, d := fromDirectory(t, out)
	if h != wantHeader || d != wantDigest {
		t.Errorf("the pulled directory is the image %s of %+v, want %s of %+v", d, h, wantDigest, wantHeader)
	}
	got, err := ociimage.ReadContractDir(out)
	if err != nil {
		t.Fatal(err)
	}
	want, err := ociimage.ReadContractDir(built)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the pulled contract is %+v, want %+v", got, want)
	}

	parts, err := image.ReadPartitions(filepath.Join(out, "repart-output.json"))
	if err != nil {
		t.Fatal(err)
	}
	id := h.RootHash[:16]
	wantParts := []image.Partition{
		{Type: "esp", Label: "esp", Offset: 1 << 20, RawSize: 4 << 20},
		{Type: "usr-x86-64-verity", Label: "store-verity_" + id, Offset: 5 << 20, RawSize: 64 << 10, RootHash: h.RootHash},
		{Type: "usr-x86-64", Label: "store_" + id, Offset: 5<<20 + 64<<10, RawSize: 155648, RootHash: h.RootHash},
	}
	if !reflect.DeepEqual(parts, wantParts) {
		t.Errorf("repart-output.json lists %+v, want %+v", parts, wantParts)
	}
	imagetest.NeedTools(t, "sfdisk")
	table, err := exec.Command("sfdisk", "--json", filepath.Join(out, "chalkos.raw")).Output()
	if err != nil {
		t.Fatal(err)
	}
	disk, err := gpt.ParseTable(filepath.Join(out, "chalkos.raw"), table)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if raw, err = os.ReadFile(filepath.Join(out, "repart-output.json")); err != nil {
		t.Fatal(err)
	}
	var listed []struct{ UUID string }
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	wantTypes := []string{gpt.TypeESP, "77ff5f63-e7b6-4633-acf4-1565b864c0e6", "8484680c-9521-48c6-9c11-b0720656f69e"}
	for i, part := range disk {
		if part.Start*512 != wantParts[i].Offset || part.Bytes() != wantParts[i].RawSize || part.Name != wantParts[i].Label || part.Type != wantTypes[i] || part.UUID != listed[i].UUID {
			t.Errorf("the GPT's partition %d is %+v, unlike repart-output.json's %+v", i+1, part, wantParts[i])
		}
	}
	if store := disk[2].UUID; store != h.RootHash[:8]+"-"+h.RootHash[8:12]+"-"+h.RootHash[12:16]+"-"+h.RootHash[16:20]+"-"+h.RootHash[20:32] {
		t.Errorf("the store partition's UUID is %s, not the root hash's first 128 bits", store)
	}
	files, err := image.ListESP(context.Background(), image.FAT(filepath.Join(out, "chalkos.raw"), 1<<20), "::/EFI/*/*.EFI")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"::/EFI/BOOT/BOOTX64.EFI", "::/EFI/Linux/chalkos_1.efi"}; !reflect.DeepEqual(files, want) {
		t.Errorf("the ESP holds %q, want %q", files, want)
	}
}

// TestDirectoryIsReproducible pulls the same image twice into the same raw disk image, byte for
// byte, whose disk GUID, ESP partition UUID and ESP volume ID are those systemd-repart gives an
// image's build.
func TestDirectoryIsReproducible(t *testing.T) {
	ref := ocitest.Registry(t) + "/lab/node-metal:v1"
	ocitest.PushDirectory(t, buildImage(t, "a"), ref)
	var sums [2][32]byte
	var raw string
	for i := range sums {
		out := filepath.Join(t.TempDir(), "node-metal")
		if _, err := imagepull.Directory(context.Background(), ref, out); err != nil {
			t.Fatal(err)
		}
		raw = filepath.Join(out, "chalkos.raw")
		data, err := os.ReadFile(raw)
		if err != nil {
			t.Fatal(err)
		}
		sums[i] = sha256.Sum256(data)
	}
	if sums[0] != sums[1] {
		t.Errorf("two pulls of the same image wrote chalkos.raw of SHA-256 %x and %x", sums[0], sums[1])
	}

	imagetest.NeedTools(t, "sfdisk")
	dump, err := exec.Command("sfdisk", "--dump", raw).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"label-id: B581FEF7-24ED-4F31-990B-099EC86BBA03", "uuid=DAC4058A-2FC0-4808-A7EF-B8B4501C00CE, name=\"esp\""} {
		if !strings.Contains(string(dump), want) {
			t.Errorf("sfdisk --dump prints no %q:\n%s", want, dump)
		}
	}
	info, err := image.Mtools(context.Background(), "mdir", "-i", image.FAT(raw, 1<<20), "::/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(info, "Volume Serial Number is 793A-442B") {
		t.Errorf("the ESP's volume ID is not repart's:\n%s", info)
	}
}

// TestDirectoryNeedsMtools refuses to pull into an image directory without mtools on PATH before
// it asks the registry for anything.
func TestDirectoryNeedsMtools(t *testing.T) {
	host, manifests := ocitest.CountingRegistry(t)
	t.Setenv("PATH", t.TempDir())
	_, err := imagepull.Directory(context.Background(), host+"/lab/node-metal:v1", filepath.Join(t.TempDir(), "out"))
	if err == nil || !strings.Contains(err.Error(), "mformat, mmd, mcopy, mdir PATH lacks; install mtools") {
		t.Errorf("err = %v, want mtools named", err)
	}
	if n := manifests.Load(); n != 0 {
		t.Errorf("the pull asked the registry for %d manifests before it found mtools missing", n)
	}
}

// TestLayoutKeepsTheDigest pulls an image by tag into the OCI layout of its manifest, which reads
// as the image of the digest pushed, and pushes it, as chalkbuild push does, to the same digest.
func TestLayoutKeepsTheDigest(t *testing.T) {
	host := ocitest.Registry(t)
	pinned := ocitest.PushDirectory(t, buildImage(t, "a"), host+"/lab/node-metal:v1")
	out := filepath.Join(t.TempDir(), "node-metal")

	p, err := imagepull.Layout(context.Background(), host+"/lab/node-metal:v1", out)
	if err != nil {
		t.Fatal(err)
	}
	if p.Reference != pinned {
		t.Errorf("pulled %s, want %s", p.Reference, pinned)
	}
	img, err := ociimage.ReadLayout(out)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := img.Digest(); host+"/lab/node-metal@"+d.String() != pinned {
		t.Errorf("the layout holds %s, want %s", d, pinned)
	}
	pushed, err := ociimage.Push(context.Background(), img, host+"/site/node-metal:v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, digest, _ := strings.Cut(pinned, "@"); pushed != host+"/site/node-metal@"+digest {
		t.Errorf("pushing the layout gave %s, want the digest of %s", pushed, pinned)
	}
}

// TestPullRefusesACorruptedLayer refuses a store layer the registry serves with other bytes than
// its digest names, and leaves no directory behind.
func TestPullRefusesACorruptedLayer(t *testing.T) {
	built := buildImage(t, "a")
	img, closeImage, err := ociimage.FromDirectory(context.Background(), built)
	if err != nil {
		t.Fatal(err)
	}
	defer closeImage()
	store, err := ociimage.Layer(img, ociimage.StoreType)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := store.Digest()
	host := ocitest.RegistryWith(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/blobs/"+digest.String()) {
			return false
		}
		rc, _ := store.Compressed()
		data, _ := io.ReadAll(rc)
		rc.Close()
		data[0] ^= 0xff
		w.Header().Set("Docker-Content-Digest", digest.String())
		w.Write(data)
		return true
	})
	ref := host + "/lab/node-metal:v1"
	ocitest.Push(t, img, ref)
	for name, pull := range map[string]func(context.Context, string, string) (imagepull.Pulled, error){"directory": imagepull.Directory, "layout": imagepull.Layout} {
		parent := t.TempDir()
		_, err := pull(context.Background(), ref, filepath.Join(parent, "out"))
		if err == nil || !strings.Contains(err.Error(), digest.Hex) {
			t.Errorf("%s: err = %v, want the store's digest refused", name, err)
		}
		assertEmpty(t, parent)
	}
}

// TestPullRefusesAStoreUnlikeItsHashTree refuses an image whose store matches its digest but not
// its hash tree, as a node does.
func TestPullRefusesAStoreUnlikeItsHashTree(t *testing.T) {
	img, closeImage, err := ociimage.FromDirectory(context.Background(), buildImage(t, "a"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeImage()
	store, err := ociimage.Layer(img, ociimage.StoreType)
	if err != nil {
		t.Fatal(err)
	}
	rc, _ := store.Compressed()
	data, _ := io.ReadAll(rc)
	rc.Close()
	data[len(data)-1] ^= 0xff
	tampered, err := ociimage.Replace(img, ociimage.BytesBlob(ociimage.StoreType, data))
	if err != nil {
		t.Fatal(err)
	}
	ref := ocitest.Registry(t) + "/lab/node-metal:v1"
	ocitest.Push(t, tampered, ref)
	for name, pull := range map[string]func(context.Context, string, string) (imagepull.Pulled, error){"directory": imagepull.Directory, "layout": imagepull.Layout} {
		parent := t.TempDir()
		_, err := pull(context.Background(), ref, filepath.Join(parent, "out"))
		if err == nil || !strings.Contains(err.Error(), "its store does not match its hash tree") {
			t.Errorf("%s: err = %v, want the store refused", name, err)
		}
		assertEmpty(t, parent)
	}
}

// TestPullOverwritesNothing pulls into an empty directory, leaves a directory holding the image
// already as it is, and refuses one holding another image or anything else.
func TestPullOverwritesNothing(t *testing.T) {
	host := ocitest.Registry(t)
	ref := host + "/lab/node-metal:v1"
	ocitest.PushDirectory(t, buildImage(t, "a"), ref)
	other := ocitest.PushDirectory(t, buildImage(t, "b"), host+"/lab/node-metal:v2")
	ctx := context.Background()

	for name, pull := range map[string]func(context.Context, string, string) (imagepull.Pulled, error){"directory": imagepull.Directory, "layout": imagepull.Layout} {
		out := filepath.Join(t.TempDir(), "out")
		if err := os.Mkdir(out, 0o755); err != nil {
			t.Fatal(err)
		}
		if p, err := pull(ctx, ref, out); err != nil || p.Present {
			t.Fatalf("%s: pulling into an empty directory: %+v, %v", name, p, err)
		}
		if p, err := pull(ctx, ref, out); err != nil || !p.Present {
			t.Errorf("%s: pulling the same image again: %+v, %v, want it left as present", name, p, err)
		}
		_, digest, _ := strings.Cut(other, "@")
		if _, err := pull(ctx, host+"/lab/node-metal:v2", out); err == nil || !strings.Contains(err.Error(), "not "+digest) {
			t.Errorf("%s: pulling another image over it: %v", name, err)
		}

		busy := filepath.Join(t.TempDir(), "busy")
		if err := os.MkdirAll(busy, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(busy, "notes.txt"), []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := pull(ctx, ref, busy); err == nil || !strings.Contains(err.Error(), "is not empty and holds no image") {
			t.Errorf("%s: pulling into a directory of other files: %v", name, err)
		}
		if data, err := os.ReadFile(filepath.Join(busy, "notes.txt")); err != nil || !bytes.Equal(data, []byte("mine")) {
			t.Errorf("%s: the other files changed: %q, %v", name, data, err)
		}
	}
}

// assertEmpty fails unless dir is empty: a failed pull leaves neither its directory nor a
// temporary one.
func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 0 {
		t.Errorf("%s holds %v after a failed pull", dir, entries)
	}
}
