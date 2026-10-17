package chalkd

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/trevex/chalkos/pkg/config"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
)

// A fact a rule reads that cannot be loaded refuses the config, and changes nothing.
func TestApplyConfigRefusesWhenAFactCannotBeLoaded(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	write(t, s.Kubernetes.Paths.Share(), "{")
	r.calls = nil
	_, err := sendConfig(s, workerConfig)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "cannot check: fact share: ") || !strings.Contains(err.Error(), "share.json is not JSON") {
		t.Fatalf("err = %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %v", r.calls)
	}
}

// stdinRecorder answers every command fact with null and keeps what it read.
type stdinRecorder struct{ stdin []byte }

func (r *stdinRecorder) Run(_ context.Context, _ []string, stdin []byte, _ int) ([]byte, error) {
	r.stdin = stdin
	return []byte("null"), nil
}

// Command facts read the config with its secret values hashed, the Kubernetes Secrets an Addon
// applies among them.
func TestCommandFactsReadSecretsHashed(t *testing.T) {
	s, _ := configServer(t)
	rec := &stdinRecorder{}
	s.Facts = rec
	write(t, s.Paths.Facts, `{"probe": {"command": "probe"}}`)
	docs, err := config.Parse(strings.NewReader(`apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Addon
metadata: {name: a}
spec:
  objects:
  - {apiVersion: v1, kind: Secret, metadata: {name: k}, data: {token: plain-token}, stringData: {other: plain-other}}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: s}
spec: {data: {token: plain-secret}}
`))
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.factsFor(context.Background(), docs, docs, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Fact("probe"); err != nil {
		t.Fatal(err)
	}
	if len(rec.stdin) == 0 || strings.Contains(string(rec.stdin), "plain-") {
		t.Errorf("the command read %s", rec.stdin)
	}
}

// The share fact holds the share's kind alone, none of its keys.
func TestShareFactHoldsNoKeys(t *testing.T) {
	s, _ := clusterServer(t, k8s.KindControlPlane)
	f, err := s.factsFor(context.Background(), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.Fact("share")
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"kind": k8s.KindControlPlane}; !reflect.DeepEqual(v, want) {
		t.Errorf("share = %v, want %v", v, want)
	}
}
