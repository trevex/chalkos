package apply

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"
)

// fakeAPIServer serves discovery for v1 ConfigMaps and Namespaces and, once a definition was
// applied, example.com/v1 Widgets; it records the requests that apply objects.
type fakeAPIServer struct {
	mu      sync.Mutex
	applied []string
	widgets bool
	ready   bool
}

func (f *fakeAPIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	write := func(v any) { json.NewEncoder(w).Encode(v) }
	resource := func(name, kind string, namespaced bool) map[string]any {
		return map[string]any{"name": name, "kind": kind, "namespaced": namespaced, "verbs": []string{"get", "patch"}}
	}
	switch {
	case r.URL.Path == "/readyz":
		w.Header().Set("Content-Type", "text/plain")
		if !f.ready {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, "[-]etcd failed")
			f.ready = true
			return
		}
		io.WriteString(w, "ok")
	case r.URL.Path == "/api":
		write(map[string]any{"kind": "APIVersions", "versions": []string{"v1"}})
	case r.URL.Path == "/apis":
		groups := []any{map[string]any{"name": "apiextensions.k8s.io", "versions": []any{map[string]any{"groupVersion": "apiextensions.k8s.io/v1", "version": "v1"}}}}
		if f.widgets {
			groups = append(groups, map[string]any{"name": "example.com", "versions": []any{map[string]any{"groupVersion": "example.com/v1", "version": "v1"}}})
		}
		write(map[string]any{"kind": "APIGroupList", "apiVersion": "v1", "groups": groups})
	case r.URL.Path == "/api/v1":
		write(map[string]any{"kind": "APIResourceList", "groupVersion": "v1", "resources": []any{
			resource("configmaps", "ConfigMap", true), resource("namespaces", "Namespace", false),
		}})
	case r.URL.Path == "/apis/apiextensions.k8s.io/v1":
		write(map[string]any{"kind": "APIResourceList", "groupVersion": "apiextensions.k8s.io/v1", "resources": []any{
			resource("customresourcedefinitions", "CustomResourceDefinition", false),
		}})
	case r.URL.Path == "/apis/example.com/v1" && f.widgets:
		write(map[string]any{"kind": "APIResourceList", "groupVersion": "example.com/v1", "resources": []any{
			resource("widgets", "Widget", true),
		}})
	case r.Method == http.MethodPatch:
		body, _ := io.ReadAll(r.Body)
		q := r.URL.Query()
		if r.Header.Get("Content-Type") != "application/apply-patch+yaml" || q.Get("fieldManager") != FieldManager || q.Get("force") != "true" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.applied = append(f.applied, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/customresourcedefinitions/widgets.example.com") {
			f.widgets = true
		}
		w.Write(body)
	default:
		w.WriteHeader(http.StatusNotFound)
		write(map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": "NotFound", "code": 404})
	}
}

func startAPIServer(t *testing.T) (*fakeAPIServer, *rest.Config) {
	t.Helper()
	f := &fakeAPIServer{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, &rest.Config{Host: srv.URL}
}

func TestApplyInOrderWithDefinitions(t *testing.T) {
	f, cfg := startAPIServer(t)
	a, err := NewApplier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.NoMatchRetry = 10 * time.Second
	objects := []map[string]any{
		{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "apps"}},
		{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "settings", "namespace": "apps"}},
		{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "plain"}},
		{"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition", "metadata": map[string]any{"name": "widgets.example.com"}},
		// The API server serves Widgets only once their definition is applied.
		{"apiVersion": "example.com/v1", "kind": "Widget", "metadata": map[string]any{"name": "w", "namespace": "apps"}},
	}
	if err := a.Apply(context.Background(), objects); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/api/v1/namespaces/apps",
		"/api/v1/namespaces/apps/configmaps/settings",
		"/api/v1/namespaces/default/configmaps/plain",
		"/apis/apiextensions.k8s.io/v1/customresourcedefinitions/widgets.example.com",
		"/apis/example.com/v1/namespaces/apps/widgets/w",
	}
	if strings.Join(f.applied, "\n") != strings.Join(want, "\n") {
		t.Errorf("applied\n%s\nwant\n%s", strings.Join(f.applied, "\n"), strings.Join(want, "\n"))
	}
}

func TestApplyRefusesUnknownKindAndBrokenObjects(t *testing.T) {
	_, cfg := startAPIServer(t)
	a, err := NewApplier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.NoMatchRetry = 0
	if err := a.Apply(context.Background(), []map[string]any{{"apiVersion": "example.com/v1", "kind": "Gadget", "metadata": map[string]any{"name": "g"}}}); err == nil || !strings.Contains(err.Error(), "Gadget g") {
		t.Errorf("err = %v, want an error naming Gadget g", err)
	}
	if err := a.Apply(context.Background(), []map[string]any{{"kind": "ConfigMap"}}); err == nil {
		t.Error("applied an object without apiVersion and name")
	}
}

func TestWaitReady(t *testing.T) {
	f, cfg := startAPIServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := WaitReady(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if !f.ready {
		t.Error("returned before /readyz answered ok")
	}
}

func TestReadManifests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifests.json")
	if err := os.WriteFile(path, []byte(`[{"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": "apps"}}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	objects, err := ReadManifests(path)
	if err != nil || len(objects) != 1 || objects[0]["kind"] != "Namespace" {
		t.Fatalf("ReadManifests() = %v, %v", objects, err)
	}
}
