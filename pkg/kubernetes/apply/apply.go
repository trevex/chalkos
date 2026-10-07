// Package apply talks to the cluster from a control-plane node: it waits for the API server,
// applies manifests with server-side apply, and approves kubelet serving certificates.
package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

// FieldManager owns the fields chalkd applies.
const FieldManager = "chalkd"

// ReadManifests reads the image's list of objects, as Nix renders it.
func ReadManifests(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var objects []map[string]any
	if err := json.Unmarshal(data, &objects); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return objects, nil
}

// Applier applies objects with server-side apply.
type Applier struct {
	dynamic dynamic.Interface
	mapper  meta.ResettableRESTMapper
	// NoMatchRetry is how long Apply waits for the API server to serve a kind it does not know
	// yet, such as a custom resource whose definition was applied just before.
	NoMatchRetry time.Duration
}

// NewApplier returns an applier for the cluster the configuration reaches.
func NewApplier(cfg *rest.Config) (*Applier, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Applier{
		dynamic:      dyn,
		mapper:       restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc)),
		NoMatchRetry: time.Minute,
	}, nil
}

// Apply applies the objects in order, taking over fields others manage, so the image's
// manifests always win. Objects without a namespace go to default when their kind is
// namespaced.
func (a *Applier) Apply(ctx context.Context, objects []map[string]any) error {
	for i, raw := range objects {
		obj := &unstructured.Unstructured{Object: raw}
		gvk := obj.GroupVersionKind()
		if gvk.Kind == "" || gvk.Version == "" || obj.GetName() == "" {
			return fmt.Errorf("manifest %d has no apiVersion, kind or name", i)
		}
		what := fmt.Sprintf("%s %s", gvk.Kind, obj.GetName())
		mapping, err := a.mapping(ctx, obj)
		if err != nil {
			return fmt.Errorf("apply %s: %w", what, err)
		}
		var r dynamic.ResourceInterface = a.dynamic.Resource(mapping.Resource)
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			ns := obj.GetNamespace()
			if ns == "" {
				ns = metav1.NamespaceDefault
			}
			r = a.dynamic.Resource(mapping.Resource).Namespace(ns)
		}
		if _, err := r.Apply(ctx, obj.GetName(), obj, metav1.ApplyOptions{FieldManager: FieldManager, Force: true}); err != nil {
			return fmt.Errorf("apply %s: %w", what, err)
		}
	}
	return nil
}

// mapping finds the resource of the object's kind, asking the API server again while it does
// not know the kind.
func (a *Applier) mapping(ctx context.Context, obj *unstructured.Unstructured) (*meta.RESTMapping, error) {
	gvk := obj.GroupVersionKind()
	deadline := time.Now().Add(a.NoMatchRetry)
	for {
		mapping, err := a.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err == nil || !meta.IsNoMatchError(err) || time.Now().After(deadline) {
			return mapping, err
		}
		a.mapper.Reset()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// WaitReady polls the API server's /readyz until it answers ok or ctx ends.
func WaitReady(ctx context.Context, cfg *rest.Config) error {
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return err
	}
	client := dc.RESTClient()
	var last error
	for {
		body, err := client.Get().AbsPath("/readyz").Timeout(5 * time.Second).DoRaw(ctx)
		if err == nil && string(body) == "ok" {
			return nil
		}
		if err == nil {
			err = fmt.Errorf("/readyz answered %q", body)
		}
		last = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("the API server is not ready: %w", last)
		case <-time.After(2 * time.Second):
		}
	}
}
