package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/pki"
	"github.com/trevex/chalkos/pkg/registry"
	"github.com/trevex/chalkos/pkg/statusview"
)

// TestDeleteOnEndpoints deletes a tag on every endpoint, counts one that never held it as done,
// names an endpoint it could not reach, and deletes a digest with its tag.
func TestDeleteOnEndpoints(t *testing.T) {
	te := startEndpoints(t, nil, "192.0.2.1", "192.0.2.2", "192.0.2.3")
	img := randomImage(t)
	te.pushTo(t, img, "docker.io/library/busybox:1.37.0", "192.0.2.1", "192.0.2.2")
	var out bytes.Buffer
	if err := te.delete(context.Background(), &out, ref(t, "busybox:1.37.0")); err != nil {
		t.Fatalf("delete = %v; %s", err, out.String())
	}
	if want := "192.0.2.1: deleted 192.0.2.1:5000/docker.io/library/busybox:1.37.0\n" +
		"192.0.2.2: deleted 192.0.2.2:5000/docker.io/library/busybox:1.37.0\n" +
		"192.0.2.3: absent 192.0.2.3:5000/docker.io/library/busybox:1.37.0\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
	for _, e := range []string{"192.0.2.1", "192.0.2.2"} {
		if d, ok := te.digestAt(t, e, "docker.io/library/busybox:1.37.0"); ok {
			t.Errorf("%s still serves the tag, at %s", e, d)
		}
	}

	// A digest takes the manifest and its tag.
	te.pushTo(t, img, "docker.io/library/busybox:1.37.0", "192.0.2.1", "192.0.2.2", "192.0.2.3")
	out.Reset()
	if err := te.delete(context.Background(), &out, ref(t, "docker.io/library/busybox@"+digestOf(t, img).String())); err != nil {
		t.Fatalf("delete by digest = %v; %s", err, out.String())
	}
	for _, e := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"} {
		if d, ok := te.digestAt(t, e, "docker.io/library/busybox@"+digestOf(t, img).String()); ok {
			t.Errorf("%s still serves the manifest, at %s", e, d)
		}
		if d, ok := te.digestAt(t, e, "docker.io/library/busybox:1.37.0"); ok {
			t.Errorf("%s still serves the tag of the deleted manifest, at %s", e, d)
		}
	}

	// A reader may not delete; the registry says so for each endpoint.
	te.pushTo(t, img, "docker.io/library/busybox:1.37.0", "192.0.2.1")
	te.tls = te.clientTLS(t, pki.RoleReader)
	out.Reset()
	if err := te.delete(context.Background(), &out, ref(t, "busybox:1.37.0")); err == nil || !strings.Contains(out.String(), "192.0.2.1: failed: ") || !strings.Contains(out.String(), "admin role") {
		t.Errorf("delete as a reader = %v, %q, want the registry's refusal", err, out.String())
	}

	te.tls = te.clientTLS(t, pki.RoleAdmin)
	te.regs[netip.MustParseAddr("192.0.2.2")].Close()
	out.Reset()
	err := te.delete(context.Background(), &out, ref(t, "busybox:1.37.0"))
	if want := "the image may still be on 1 of 3 endpoints of the cluster registry: 192.0.2.2; delete it again once they are reachable"; err == nil || err.Error() != want {
		t.Errorf("delete with 192.0.2.2 stopped = %v, want %q", err, want)
	}
	if !strings.Contains(out.String(), "192.0.2.1: deleted ") || !strings.Contains(out.String(), "192.0.2.2: failed: ") {
		t.Errorf("output = %q, want 192.0.2.1 deleted and 192.0.2.2 failed", out.String())
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

// TestCollectOnEndpoints collects garbage on every endpoint, reports the space each freed, and
// names an endpoint that collects already.
func TestCollectOnEndpoints(t *testing.T) {
	te := startEndpoints(t, map[string]func(http.Handler) http.Handler{"192.0.2.3": collecting}, "192.0.2.1", "fd00::2", "192.0.2.3")
	img := randomImage(t)
	te.pushTo(t, img, "docker.io/library/busybox:1.37.0", "192.0.2.1", "fd00::2")
	var out bytes.Buffer
	if err := te.delete(context.Background(), &out, ref(t, "busybox@"+digestOf(t, img).String())); err != nil {
		t.Fatalf("delete = %v; %s", err, out.String())
	}
	out.Reset()
	err := te.collect(context.Background(), &out)
	if want := "garbage collection failed on 1 of 3 endpoints of the cluster registry: 192.0.2.3"; err == nil || err.Error() != want {
		t.Errorf("collect = %v, want %q", err, want)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "192.0.2.1: freed ") || !strings.HasSuffix(lines[0], " KiB") ||
		!strings.HasPrefix(lines[1], "fd00::2: freed ") || lines[2] != "192.0.2.3: failed: the registry is collecting garbage already" {
		t.Errorf("output = %q, want space freed on two endpoints and the third collecting already", out.String())
	}

	te.tls = te.clientTLS(t, pki.RoleReader)
	out.Reset()
	if err := te.collect(context.Background(), &out); err == nil || !strings.Contains(out.String(), "192.0.2.1: failed: writing to the registry needs a client certificate of the admin role") {
		t.Errorf("collect as a reader = %v, %q, want the registry's refusal", err, out.String())
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
// go-containerregistry would speak plain HTTP to, and marks those missing on or differing between
// endpoints.
func TestListEndpoints(t *testing.T) {
	te := startEndpoints(t, nil, "10.0.0.1", "10.0.0.2", "10.0.0.3")
	// Pages of one repository and one tag.
	te.pageSize = 1
	a, b, other := randomImage(t), randomImage(t), randomImage(t)
	te.pushTo(t, a, "docker.io/library/a:1", "10.0.0.1", "10.0.0.2", "10.0.0.3")
	te.pushTo(t, a, "docker.io/library/a:2", "10.0.0.1", "10.0.0.2", "10.0.0.3")
	te.pushTo(t, b, "docker.io/library/b:1", "10.0.0.1")
	te.pushTo(t, other, "docker.io/library/b:1", "10.0.0.2")
	var out bytes.Buffer
	err := te.list(context.Background(), statusview.Table(&out))
	if want := "the endpoints of the cluster registry differ in 1 of 3 images; push those again to make them alike"; err == nil || err.Error() != want {
		t.Errorf("list = %v, want %q", err, want)
	}
	want := strings.Join([]string{
		"REPOSITORY           TAG  DIGEST               ENDPOINTS",
		"docker.io/library/a  1    " + short(digestOf(t, a)) + "  all",
		"docker.io/library/a  2    " + short(digestOf(t, a)) + "  all",
		"docker.io/library/b  1    " + short(digestOf(t, b)) + "  10.0.0.1; differs on 10.0.0.2 (" + short(digestOf(t, other)) + "); missing on 10.0.0.3",
		"",
	}, "\n")
	if out.String() != want {
		t.Errorf("list =\n%s\nwant\n%s", out.String(), want)
	}

	// Alike once b:1 is pushed everywhere.
	te.pushTo(t, b, "docker.io/library/b:1", "10.0.0.2", "10.0.0.3")
	out.Reset()
	if err := te.list(context.Background(), statusview.Table(&out)); err != nil || strings.Count(out.String(), "  all\n") != 3 {
		t.Errorf("list = %v, %q, want every image on all endpoints", err, out.String())
	}

	// An endpoint that cannot be listed is named before the table, and not marked missing.
	te.regs[netip.MustParseAddr("10.0.0.3")].Close()
	out.Reset()
	err = te.list(context.Background(), statusview.Table(&out))
	if want := "1 of 3 endpoints of the cluster registry could not be listed: 10.0.0.3"; err == nil || err.Error() != want {
		t.Errorf("list with 10.0.0.3 stopped = %v, want %q", err, want)
	}
	if !strings.HasPrefix(out.String(), "10.0.0.3: failed: ") || strings.Contains(out.String(), "missing") || !strings.Contains(out.String(), "  10.0.0.1, 10.0.0.2\n") {
		t.Errorf("output = %q, want 10.0.0.3 failed first and the images on the others", out.String())
	}
}
