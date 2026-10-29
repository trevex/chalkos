package chalkd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/install"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
)

// A fact a rule reads that cannot be loaded refuses the config, and changes nothing.
func TestApplyConfigRefusesWhenAFactCannotBeLoaded(t *testing.T) {
	s, r := clusterServer(t, k8s.KindWorker)
	write(t, kubernetesPaths(s).Share(), "{")
	r.Calls = nil
	_, err := sendConfig(s, workerConfig)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "cannot check: fact share: ") || !strings.Contains(err.Error(), "share.json is not JSON") {
		t.Fatalf("err = %v", err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("ran %v", r.Calls)
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

// The share fact holds the share's kind and the SHA-256 of each of its other keys, none of its
// certificates and keys.
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
	share, _ := v.(map[string]any)
	if share["kind"] != k8s.KindControlPlane || share["nodeCA"] == nil {
		t.Fatalf("share = %v, want its kind and the hash of its node CA", v)
	}
	for key, value := range share {
		if h, ok := value.(string); key != "kind" && (!ok || len(h) != 64 || strings.Trim(h, "0123456789abcdef") != "") {
			t.Errorf("share[%s] = %v, want a hex SHA-256", key, value)
		}
	}
}

// withBrokenRule is the schema raw with a kind's rule that does not compile.
func withBrokenRule(t *testing.T, raw []byte) []byte {
	t.Helper()
	var sch map[string]any
	if err := json.Unmarshal(raw, &sch); err != nil {
		t.Fatal(err)
	}
	sch["x-chalkos-kind"].(map[string]any)["rules"] = []any{map[string]any{"rule": "documents =="}}
	data, err := json.Marshal(sch)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Rules of the running image that do not compile are the node's failure, not the request's.
func TestApplyConfigRunningRulesThatDoNotCompile(t *testing.T) {
	s, _ := configServer(t)
	path := filepath.Join(s.Loader.Schemas, "storage.chalkos.dev_v1alpha1_SystemDisk.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(withBrokenRule(t, raw)))
	_, err = sendConfig(s, baseConfig)
	if connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), "the running image's rules: SystemDisk: rule ") {
		t.Errorf("err = %v", err)
	}
}

// Rules of an image the request brings that do not compile, as an installer's install carries
// them, are the request's failure, and install nothing.
func TestInstallRulesThatDoNotCompile(t *testing.T) {
	s, _ := newTestServer(t, maintenance, vda)
	s.Installer = true
	s.FromParts = func(context.Context, install.PartsRequest) error {
		t.Error("installed onto the disk")
		return nil
	}
	h := header(t, &nodev1.InstallHeader_Disk{Disk: &nodev1.DiskReference{Serial: "chalk-target"}})
	h.Image = &nodev1.ImageHeader{ImageId: "chalkos", Platform: "metal", Architecture: "x86-64", RootHash: []byte{1}}
	contractOf(h).Schemas = fixtureSchemas(t)
	contractOf(h).Schemas["storage.chalkos.dev_v1alpha1_SystemDisk.json"] = withBrokenRule(t, contractOf(h).Schemas["storage.chalkos.dev_v1alpha1_SystemDisk.json"])
	err := sendInstall(t, s, h, nil)
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "the image's rules: SystemDisk: rule ") {
		t.Errorf("err = %v", err)
	}
}
