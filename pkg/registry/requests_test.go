package registry_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/trevex/chalkos/pkg/internal/testwait"
)

// send sends one request as who with body and returns the answer, its body read and closed.
func (r *testRegistry) send(t *testing.T, method, path, who string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, "https://"+r.Host+path, body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := r.clients[who].RoundTrip(req)
	if err != nil {
		t.Fatalf("%s %s as %s: %v", method, path, who, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

// upload begins an upload to repo as the admin and returns where it continues.
func (r *testRegistry) upload(t *testing.T, repo string) string {
	t.Helper()
	resp := r.send(t, http.MethodPost, "/v2/"+repo+"/blobs/uploads/", "admin", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("begin an upload: %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	return strings.TrimPrefix(loc, "https://"+r.Host)
}

// signalBody calls read before its first Read.
type signalBody struct {
	io.ReadCloser
	read func()
}

func (b signalBody) Read(p []byte) (int, error) {
	b.read()
	return b.ReadCloser.Read(p)
}

// TestCollectWaitsForWrites waits for an upload whose data is on its way, refusing other writes
// meanwhile, and runs once it is done.
func TestCollectWaitsForWrites(t *testing.T) {
	reading := make(chan struct{})
	var once sync.Once
	r := startWrapped(t, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method == http.MethodPatch {
				req.Body = signalBody{req.Body, func() { once.Do(func() { close(reading) }) }}
			}
			h.ServeHTTP(w, req)
		})
	})
	loc := r.upload(t, "docker.io/library/slow")
	data, sender := io.Pipe()
	req, err := http.NewRequest(http.MethodPatch, "https://"+r.Host+loc, data)
	if err != nil {
		t.Fatal(err)
	}
	patched := make(chan int, 1)
	go func() {
		resp, err := r.clients["admin"].RoundTrip(req)
		if err != nil {
			patched <- 0
			return
		}
		resp.Body.Close()
		patched <- resp.StatusCode
	}()
	<-reading
	collected := make(chan error, 1)
	go func() {
		_, err := r.Collect(context.Background())
		collected <- err
	}()
	testwait.Eventually(t, "the collection to wait for the upload", func() bool {
		return r.send(t, http.MethodPost, "/v2/docker.io/library/other/blobs/uploads/", "admin", nil).StatusCode == http.StatusServiceUnavailable
	})
	select {
	case err := <-collected:
		t.Fatalf("the collection ran during the upload: %v", err)
	default:
	}
	if _, err := sender.Write([]byte("layer")); err != nil {
		t.Fatal(err)
	}
	sender.Close()
	if status := <-patched; status != http.StatusAccepted {
		t.Errorf("the upload: %d", status)
	}
	if err := <-collected; err != nil {
		t.Errorf("the collection: %v", err)
	}
}

// TestWritesDuringCollection refuses every kind of write while a collection runs.
func TestWritesDuringCollection(t *testing.T) {
	r := startRegistry(t)
	loc := r.upload(t, "docker.io/library/x")
	release := r.HoldCollection()
	defer release()
	sep := "?"
	if strings.Contains(loc, "?") {
		sep = "&"
	}
	for _, w := range []struct{ method, path string }{
		{http.MethodPatch, loc},
		{http.MethodPut, loc + sep + "digest=sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{http.MethodDelete, "/v2/docker.io/library/x/manifests/sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
	} {
		if got := r.send(t, w.method, w.path, "admin", nil).StatusCode; got != http.StatusServiceUnavailable {
			t.Errorf("%s during a collection: %d, want 503", w.method, got)
		}
	}
}

// TestRequestsAtTheEdges answers requests of odd methods and paths: a method other than GET and
// HEAD, in any spelling, is a write; the collection's path is matched exactly, and the path
// cleaning of distribution's router redirects.
func TestRequestsAtTheEdges(t *testing.T) {
	r := startRegistry(t)
	for _, tc := range []struct {
		method, path, who string
		want              int
	}{
		{http.MethodOptions, "/v2/", "anyone", http.StatusUnauthorized},
		{"get", "/v2/", "anyone", http.StatusUnauthorized},
		{"PROPFIND", "/v2/", "anyone", http.StatusUnauthorized},
		{"PROPFIND", "/v2/", "reader", http.StatusForbidden},
		{http.MethodGet, "/v2/../chalkos/v1/gc", "anyone", http.StatusMovedPermanently},
		{http.MethodPost, "/v2/../chalkos/v1/gc", "anyone", http.StatusUnauthorized},
		{http.MethodPost, "/v2/../chalkos/v1/gc", "admin", http.StatusMovedPermanently},
		{http.MethodPost, "/chalkos/v1/gc/", "anyone", http.StatusUnauthorized},
		{http.MethodPost, "/chalkos/v1/gc/", "admin", http.StatusNotFound},
		{http.MethodPost, "//chalkos/v1/gc", "admin", http.StatusMovedPermanently},
		{http.MethodPost, "/chalkos/v1/gc?now=1", "reader", http.StatusForbidden},
		{http.MethodPost, "/chalkos/v1/gc?now=1", "admin", http.StatusOK},
		{http.MethodPost, "/CHALKOS/v1/gc", "admin", http.StatusNotFound},
		{http.MethodGet, "/CHALKOS/v1/gc", "anyone", http.StatusNotFound},
	} {
		if got := r.send(t, tc.method, tc.path, tc.who, nil).StatusCode; got != tc.want {
			t.Errorf("%s %s as %s: %d, want %d", tc.method, tc.path, tc.who, got, tc.want)
		}
	}
}

// TestQuietLogsOfRequests logs nothing of the requests anyone can make that the registry
// refuses: unknown images, invalid names and pages.
func TestQuietLogsOfRequests(t *testing.T) {
	r := startRegistry(t)
	var out bytes.Buffer
	std := logrus.StandardLogger()
	saved := std.Out
	std.SetOutput(&out)
	defer std.SetOutput(saved)
	for _, path := range []string{
		"/v2/docker.io/library/missing/manifests/1",
		"/v2/docker.io/library/missing/blobs/sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"/v2/Invalid..Name/tags/list",
		"/v2/_catalog?n=-1",
		"/v2/_catalog?n=100000",
	} {
		if got := r.send(t, http.MethodGet, path, "anyone", nil).StatusCode; got < 400 || got >= 500 {
			t.Errorf("GET %s: %d, want a client error", path, got)
		}
	}
	if out.Len() != 0 {
		t.Errorf("logged:\n%s", out.String())
	}
}
