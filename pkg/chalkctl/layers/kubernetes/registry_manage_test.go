package kubernetes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/registry"
	"github.com/trevex/chalkos/pkg/statusview"
)

// TestDeleteTag deletes a tag on every endpoint, counts one that never held it as done, says
// which tags still name its manifest, and names an endpoint it could not reach.
func TestDeleteTag(t *testing.T) {
	te := startEndpoints(t, nil, "192.0.2.1", "192.0.2.2", "192.0.2.3")
	img := randomImage(t)
	d := digestOf(t, img).String()
	te.pushTo(t, img, "docker.io/library/busybox:1.37.0", "192.0.2.1", "192.0.2.2")
	te.pushTo(t, img, "docker.io/library/busybox:latest", "192.0.2.2")
	if err := te.delete(context.Background(), &te.out, &te.errOut, iref(t, "busybox:1.37.0"), false, accept); err != nil {
		t.Fatalf("delete = %v; %s", err, te.errOut.String())
	}
	if want := "192.0.2.1: deleted 192.0.2.1:5000/docker.io/library/busybox:1.37.0, which named " + d + "; no tag names it now\n" +
		"192.0.2.2: deleted 192.0.2.2:5000/docker.io/library/busybox:1.37.0, which named " + d + "; the tags latest still name it\n" +
		"192.0.2.3: absent 192.0.2.3:5000/docker.io/library/busybox:1.37.0\n" +
		"the manifest " + d + " stays until chalkctl registry delete docker.io/library/busybox@" + d + "; chalkctl registry gc then frees its layers\n"; te.out.String() != want {
		t.Errorf("output =\n%s\nwant\n%s", te.out.String(), want)
	}
	for _, e := range []string{"192.0.2.1", "192.0.2.2"} {
		if got, ok := te.digestAt(t, e, "docker.io/library/busybox:1.37.0"); ok {
			t.Errorf("%s still serves the tag, at %s", e, got)
		}
		if _, ok := te.digestAt(t, e, "docker.io/library/busybox@"+d); !ok {
			t.Errorf("%s lost the manifest with its tag", e)
		}
	}

	// A reader may not delete; the registry says so for the endpoint.
	te.pushTo(t, img, "docker.io/library/busybox:1.37.0", "192.0.2.1")
	te.tls = te.clientTLS(t, pki.RoleReader)
	te.reset()
	if err := te.delete(context.Background(), &te.out, &te.errOut, iref(t, "busybox:1.37.0"), false, accept); err == nil || !strings.HasPrefix(te.errOut.String(), "192.0.2.1: failed: ") || !strings.Contains(te.errOut.String(), "admin role") {
		t.Errorf("delete as a reader = %v, %q, want the registry's refusal", err, te.errOut.String())
	}

	te.tls = te.clientTLS(t, pki.RoleAdmin)
	te.regs[netip.MustParseAddr("192.0.2.2")].Close()
	te.reset()
	err := te.delete(context.Background(), &te.out, &te.errOut, iref(t, "busybox:1.37.0"), false, accept)
	if want := "the delete failed on 1 of 3 endpoints of the cluster registry: 192.0.2.2; delete again once they are reachable"; err == nil || err.Error() != want {
		t.Errorf("delete with 192.0.2.2 stopped = %v, want %q", err, want)
	}
	if !strings.HasPrefix(te.out.String(), "192.0.2.1: deleted ") || !strings.HasPrefix(te.errOut.String(), "192.0.2.2: failed: ") {
		t.Errorf("output = %q, errors = %q, want 192.0.2.1 deleted and 192.0.2.2 failed", te.out.String(), te.errOut.String())
	}
}

// TestDeleteDigest refuses a digest that tags name unless --with-tags, before it deletes
// anything, and then deletes it with them; it refuses a digest that a tagged index names.
func TestDeleteDigest(t *testing.T) {
	te := startEndpoints(t, nil, "192.0.2.1", "192.0.2.2")
	img := randomImage(t)
	d := digestOf(t, img).String()
	te.pushTo(t, img, "docker.io/library/busybox:1.37.0", "192.0.2.1", "192.0.2.2")
	te.pushTo(t, img, "docker.io/library/busybox:latest", "192.0.2.2")
	err := te.delete(context.Background(), &te.out, &te.errOut, iref(t, "busybox@"+d), false, accept)
	if want := "the tags 1.37.0 and latest name docker.io/library/busybox@" + d + " on 192.0.2.1 and 192.0.2.2; pass --with-tags to delete them with it, or delete the tags alone"; err == nil || err.Error() != want {
		t.Errorf("delete of a tagged digest = %v, want %q", err, want)
	}
	if _, ok := te.digestAt(t, "192.0.2.1", "docker.io/library/busybox:1.37.0"); !ok || te.out.Len() != 0 {
		t.Errorf("a refused delete deleted something: %q", te.out.String())
	}
	if err := te.delete(context.Background(), &te.out, &te.errOut, iref(t, "busybox@"+d), true, accept); err != nil {
		t.Fatalf("delete --with-tags = %v; %s", err, te.errOut.String())
	}
	if want := "192.0.2.1: deleted 192.0.2.1:5000/docker.io/library/busybox@" + d + " and its tags 1.37.0\n" +
		"192.0.2.2: deleted 192.0.2.2:5000/docker.io/library/busybox@" + d + " and its tags 1.37.0 and latest\n"; te.out.String() != want {
		t.Errorf("output =\n%s\nwant\n%s", te.out.String(), want)
	}
	for _, e := range []string{"192.0.2.1", "192.0.2.2"} {
		for _, path := range []string{"docker.io/library/busybox@" + d, "docker.io/library/busybox:1.37.0", "docker.io/library/busybox:latest"} {
			if _, ok := te.digestAt(t, e, path); ok {
				t.Errorf("%s still serves %s", e, path)
			}
		}
	}

	// Absent everywhere: done.
	te.reset()
	if err := te.delete(context.Background(), &te.out, &te.errOut, iref(t, "busybox@"+d), false, accept); err != nil || strings.Count(te.out.String(), ": absent ") != 2 {
		t.Errorf("delete of an absent digest = %v, %q, want it absent on both", err, te.out.String())
	}

	// An image of a tagged index.
	idx := randomIndex(t)
	te.pushTo(t, idx, "ghcr.io/a/b:1", "192.0.2.1", "192.0.2.2")
	child := childrenOfIndex(t, idx)[0].String()
	for _, withTags := range []bool{false, true} {
		err := te.delete(context.Background(), &te.out, &te.errOut, iref(t, "ghcr.io/a/b@"+child), withTags, accept)
		if want := "ghcr.io/a/b@" + child + " is an image of the index ghcr.io/a/b:1 on 192.0.2.1 and ghcr.io/a/b:1 on 192.0.2.2; deleting it would break the index, so delete the index first"; err == nil || err.Error() != want {
			t.Errorf("delete of an index's image (--with-tags %v) = %v, want %q", withTags, err, want)
		}
	}
	if _, ok := te.digestAt(t, "192.0.2.1", "ghcr.io/a/b@"+child); !ok {
		t.Error("a refused delete deleted the index's image")
	}
}

// TestDeleteDigestAfterPrompt reads the tags again once the prompt returns, and refuses a digest
// that a tag the prompt did not name, or a tagged index, names by then.
func TestDeleteDigestAfterPrompt(t *testing.T) {
	te := startEndpoints(t, nil, "192.0.2.1", "192.0.2.2")
	img := randomImage(t)
	d := digestOf(t, img).String()
	te.pushTo(t, img, "docker.io/library/busybox:1.37.0", "192.0.2.1", "192.0.2.2")
	idx := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: img})
	for _, tc := range []struct {
		during func()
		want   string
	}{{
		during: func() { te.pushTo(t, img, "docker.io/library/busybox:latest", "192.0.2.2") },
		want:   "the tags that name docker.io/library/busybox@" + d + " on 192.0.2.2 changed while the prompt waited; delete again",
	}, {
		during: func() { te.pushTo(t, idx, "docker.io/library/busybox:multi", "192.0.2.1") },
		want:   "docker.io/library/busybox@" + d + " is an image of the index docker.io/library/busybox:multi on 192.0.2.1; deleting it would break the index, so delete the index first",
	}} {
		te.reset()
		err := te.delete(context.Background(), &te.out, &te.errOut, iref(t, "busybox@"+d), true, func(string) error { tc.during(); return nil })
		if err == nil || err.Error() != tc.want {
			t.Errorf("delete = %v, want %q", err, tc.want)
		}
		if _, ok := te.digestAt(t, "192.0.2.1", "docker.io/library/busybox@"+d); !ok || te.out.Len() != 0 {
			t.Errorf("a refused delete deleted something: %q", te.out.String())
		}
	}
}

// collecting answers a collection as a registry that runs one already does.
func collecting(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != registry.GCPath {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"code": "UNAVAILABLE", "message": registry.ErrCollecting.Error()}}})
	})
}

// TestCollectOnEndpoints collects garbage on every endpoint after a digest's delete, which frees
// its layers, and names an endpoint that collects already.
func TestCollectOnEndpoints(t *testing.T) {
	te := startEndpoints(t, map[string]func(http.Handler) http.Handler{"192.0.2.3": collecting}, "192.0.2.1", "fd00::2", "192.0.2.3")
	img := randomImage(t)
	te.pushTo(t, img, "docker.io/library/busybox:1.37.0", "192.0.2.1", "fd00::2")
	// Nothing to collect yet.
	if err := te.collect(context.Background(), &te.out, &te.errOut); err == nil || !strings.HasPrefix(te.out.String(), "192.0.2.1: freed 0 B\n") {
		t.Errorf("collect = %v, %q, want nothing freed", err, te.out.String())
	}
	if err := te.delete(context.Background(), &te.out, &te.errOut, iref(t, "busybox@"+digestOf(t, img).String()), true, accept); err != nil {
		t.Fatalf("delete = %v; %s", err, te.errOut.String())
	}
	te.reset()
	err := te.collect(context.Background(), &te.out, &te.errOut)
	if want := "garbage collection failed on 1 of 3 endpoints of the cluster registry: 192.0.2.3"; err == nil || err.Error() != want {
		t.Errorf("collect = %v, want %q", err, want)
	}
	// Two layers of 1 KiB, the config and the manifest.
	lines := strings.Split(strings.TrimSuffix(te.out.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "192.0.2.1: freed ") || !strings.HasSuffix(lines[0], " KiB") || !strings.HasPrefix(lines[1], "fd00::2: freed ") || !strings.HasSuffix(lines[1], " KiB") {
		t.Errorf("output = %q, want the image's layers freed on both endpoints", te.out.String())
	}
	layers, _ := img.Layers()
	ld, _ := layers[0].Digest()
	if _, err := remote.Head(te.at(t, "192.0.2.1", "docker.io/library/busybox@"+ld.String()), te.admin(t, "192.0.2.1")); err == nil {
		t.Error("the layer is still there after the collection")
	}
	if want := "192.0.2.3: failed: the registry is collecting garbage already\n"; te.errOut.String() != want {
		t.Errorf("errors = %q, want %q", te.errOut.String(), want)
	}

	te.tls = te.clientTLS(t, pki.RoleReader)
	te.reset()
	if err := te.collect(context.Background(), &te.out, &te.errOut); err == nil || !strings.HasPrefix(te.errOut.String(), "192.0.2.1: failed: writing to the registry needs a client certificate of the admin role") {
		t.Errorf("collect as a reader = %v, %q, want the registry's refusal", err, te.errOut.String())
	}
}

func TestSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 8 << 10: "8.0 KiB", 3<<20 + 1<<19: "3.5 MiB", 5 << 30: "5.0 GiB"} {
		if got := size(n); got != want {
			t.Errorf("size(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestListEndpoints lists the images of every endpoint, page by page, on endpoints that
// go-containerregistry would speak plain HTTP to, and marks those missing on, differing between
// or broken on endpoints, by the digest most endpoints serve.
func TestListEndpoints(t *testing.T) {
	te := startEndpoints(t, nil, "10.0.0.1", "10.0.0.2", "10.0.0.3")
	// Pages of one repository and one tag.
	te.pageSize = 1
	a, b, other := randomImage(t), randomImage(t), randomImage(t)
	te.pushTo(t, a, "docker.io/library/a:1", "10.0.0.1", "10.0.0.2", "10.0.0.3")
	te.pushTo(t, a, "docker.io/library/a:2", "10.0.0.1", "10.0.0.2", "10.0.0.3")
	te.pushTo(t, b, "docker.io/library/b:1", "10.0.0.1")
	te.pushTo(t, other, "docker.io/library/b:1", "10.0.0.2")
	// Most endpoints serve c:1 as b; 10.0.0.1 differs.
	te.pushTo(t, other, "docker.io/library/c:1", "10.0.0.1")
	te.pushTo(t, b, "docker.io/library/c:1", "10.0.0.2", "10.0.0.3")
	list := func() error {
		te.reset()
		return te.list(context.Background(), statusview.Table(&te.out), &te.errOut)
	}
	err := list()
	if want := "the endpoints of the cluster registry differ in 2 of 4 images; push those again to every endpoint to make them alike"; err == nil || err.Error() != want {
		t.Errorf("list = %v, want %q", err, want)
	}
	want := strings.Join([]string{
		"REPOSITORY           TAG  DIGEST               ENDPOINTS",
		"docker.io/library/a  1    " + short(digestOf(t, a)) + "  all",
		"docker.io/library/a  2    " + short(digestOf(t, a)) + "  all",
		"docker.io/library/b  1    " + short(digestOf(t, b)) + "  10.0.0.1; differs on 10.0.0.2 (" + short(digestOf(t, other)) + "); missing on 10.0.0.3",
		"docker.io/library/c  1    " + short(digestOf(t, b)) + "  10.0.0.2, 10.0.0.3; differs on 10.0.0.1 (" + short(digestOf(t, other)) + ")",
		"",
	}, "\n")
	if te.out.String() != want {
		t.Errorf("list =\n%s\nwant\n%s", te.out.String(), want)
	}

	// Alike once b:1 and c:1 are pushed everywhere.
	te.pushTo(t, b, "docker.io/library/b:1", "10.0.0.2", "10.0.0.3")
	te.pushTo(t, b, "docker.io/library/c:1", "10.0.0.1")
	if err := list(); err != nil || strings.Count(te.out.String(), "  all\n") != 4 {
		t.Errorf("list = %v, %q, want every image on all endpoints", err, te.out.String())
	}

	// An index whose image one endpoint lacks is broken there.
	idx := randomIndex(t)
	te.pushTo(t, idx, "ghcr.io/x/y:1", "10.0.0.1", "10.0.0.2", "10.0.0.3")
	child := childrenOfIndex(t, idx)[1]
	if err := remote.Delete(te.at(t, "10.0.0.2", "ghcr.io/x/y@"+child.String()), te.admin(t, "10.0.0.2")); err != nil {
		t.Fatal(err)
	}
	err = list()
	if want := "the endpoints of the cluster registry differ in 1 of 5 images; push those again to every endpoint to make them alike"; err == nil || err.Error() != want {
		t.Errorf("list with a broken index = %v, want %q", err, want)
	}
	if want := "ghcr.io/x/y          1    " + short(digestOf(t, idx)) + "  10.0.0.1, 10.0.0.3; broken on 10.0.0.2 (lacks 1 image of the index)\n"; !strings.HasSuffix(te.out.String(), want) {
		t.Errorf("list =\n%s\nwant it to end with\n%s", te.out.String(), want)
	}
	te.pushTo(t, idx, "ghcr.io/x/y:1", "10.0.0.2")

	// An endpoint that cannot be listed is named on errOut, and not marked missing.
	te.regs[netip.MustParseAddr("10.0.0.3")].Close()
	err = list()
	if want := "1 of 3 endpoints of the cluster registry could not be listed: 10.0.0.3"; err == nil || err.Error() != want {
		t.Errorf("list with 10.0.0.3 stopped = %v, want %q", err, want)
	}
	if !strings.HasPrefix(te.errOut.String(), "10.0.0.3: failed: ") || strings.Contains(te.out.String(), "missing") || !strings.Contains(te.out.String(), "  10.0.0.1, 10.0.0.2\n") {
		t.Errorf("output = %q, errors = %q, want 10.0.0.3 failed and the images on the others", te.out.String(), te.errOut.String())
	}
}

// accept is a confirmation that goes ahead.
func accept(string) error { return nil }
