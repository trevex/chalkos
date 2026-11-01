//go:build vmtest

package imagecheck

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	checkv1 "github.com/trevex/chalkos/pkg/api/check/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/facts"
)

// nodeFacts serve the facts of the image as chalkd serves them: its facts.json, its commands run in
// the facts sandbox with the image's store.
type nodeFacts struct{ l *facts.Loader }

func (f nodeFacts) Fact(name string, input map[string]any) (any, error) {
	return f.l.For(input).Fact(name)
}

// probeConfig is a config of the image of CHALKOS_IMAGECHECK_SYSTEM: a Demo, whose rule reads the
// fact probe, which the image's own program gives, and whose output's check refuses the message
// "refuse".
func probeConfig(t *testing.T, message string) []string {
	t.Helper()
	docs, err := config.Parse(strings.NewReader(`
apiVersion: storage.chalkos.dev/v1alpha1
kind: SystemDisk
metadata: {name: system}
spec: {disk: {path: /dev/vda}}
---
apiVersion: demo.example.org/v1alpha1
kind: Demo
metadata: {name: one}
spec: {name: ` + message + `, token: {secretRef: {name: probe, key: token}}}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: probe}
spec: {data: {token: s3cret}}
`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Encode(config.Redact(docs))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSandboxOnANode runs the checker of an image whose store is CHALKOS_IMAGECHECK_STORE, not the
// node's, through the sandbox on a node with systemd, as the image-checker check does: its
// chalkd, its renderer and its fact's program exist in that store alone. The tag vmtest keeps it out
// of go test elsewhere.
func TestSandboxOnANode(t *testing.T) {
	store, system := os.Getenv("CHALKOS_IMAGECHECK_STORE"), os.Getenv("CHALKOS_IMAGECHECK_SYSTEM")
	if store == "" || system == "" {
		t.Fatal("CHALKOS_IMAGECHECK_STORE and CHALKOS_IMAGECHECK_SYSTEM name an image's store and system")
	}
	data, err := os.ReadFile(filepath.Join(store, strings.TrimPrefix(system, "/nix/store/"), "etc/chalkos/facts.json"))
	if err != nil {
		t.Fatal(err)
	}
	defs, err := facts.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	// The system's sw/bin/chalkd links to chalkd's package, which the store holds too.
	checker := filepath.Join(store, strings.TrimPrefix(system, "/nix/store/"), CheckerPath)
	if target, err := os.Readlink(checker); err == nil {
		checker = filepath.Join(store, strings.TrimPrefix(target, "/nix/store/"))
	}
	img := Image{Store: store, System: system, Checker: checker}
	s := DefaultSandbox()
	for _, tc := range []struct {
		message, refusal string
	}{
		{"hello", ""},
		{"refuse", "output demo: its check refused the rendered files (exit 1); see the node's journal"},
	} {
		f := nodeFacts{facts.NewLoader(context.Background(), defs, facts.Sandbox{Store: store})}
		res, err := s.Run(context.Background(), img, &checkv1.Input{Node: "n1", Platform: "metal", Delivered: probeConfig(t, tc.message)}, f)
		if err != nil {
			t.Fatalf("%s: %v", tc.message, err)
		}
		if res.GetError() != "" || res.GetRefusal().GetMessage() != tc.refusal {
			t.Errorf("%s: result %v, want the refusal %q", tc.message, res, tc.refusal)
		}
		if tc.refusal == "" && len(res.GetDocuments()) != 3 {
			t.Errorf("%s: documents %v", tc.message, res.GetDocuments())
		}
	}
}
