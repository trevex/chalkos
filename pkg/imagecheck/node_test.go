//go:build vmtest

package imagecheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	checkv1 "github.com/trevex/chalkos/pkg/api/check/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/facts"
)

// nodeFacts serve the facts of the image as chalkd serves them: its facts.json, its commands run in
// the facts sandbox with the image's store. While the checker waits for the fact probe, they note
// in outsider whether another user of the node reaches the check's socket, input or result.
type nodeFacts struct {
	l        *facts.Loader
	s        Sandbox
	outsider *error
}

func (f nodeFacts) Fact(name string, input map[string]any) (any, error) {
	if name == "probe" {
		*f.outsider = outsider(f.s)
	}
	return f.l.For(input).Fact(name)
}

// outsider runs TestOutsider as nobody, while the unit runs, against the files of the check that
// root sees in place, the result root's alone.
func outsider(s Sandbox) error {
	for _, name := range []string{"facts.sock", "input", "result"} {
		if _, err := os.Stat(filepath.Join(s.Dir, name)); err != nil {
			return err
		}
	}
	if info, err := os.Stat(filepath.Join(s.Dir, "result")); err != nil || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("the result's mode: %v, %v", info.Mode(), err)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "-test.v", "-test.run", "^TestOutsider$")
	cmd.Env = append(os.Environ(), "CHALKOS_IMAGECHECK_OUTSIDER="+s.Dir+":"+s.Public)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestOutsider") {
		return fmt.Errorf("another user reaches the check: %v\n%s", err, out)
	}
	return nil
}

// TestOutsider, run by outsider as another user, connects to the facts socket and reads the input
// and the result of a running check, and passes when nothing succeeds.
func TestOutsider(t *testing.T) {
	dir, public, ok := strings.Cut(os.Getenv("CHALKOS_IMAGECHECK_OUTSIDER"), ":")
	if !ok {
		t.Skip("outsider runs it")
	}
	for _, sock := range []string{filepath.Join(dir, "facts.sock"), filepath.Join(public, "facts.sock")} {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			t.Errorf("connected to %s", sock)
		}
	}
	for _, name := range []string{"input", "result"} {
		if _, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			t.Errorf("read %s", name)
		}
	}
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
// chalkd, its validator, its renderer and its fact's program exist in that store alone. The tag
// vmtest keeps it out of go test elsewhere.
func TestSandboxOnANode(t *testing.T) {
	store, system := os.Getenv("CHALKOS_IMAGECHECK_STORE"), os.Getenv("CHALKOS_IMAGECHECK_SYSTEM")
	if store == "" || system == "" {
		t.Fatal("CHALKOS_IMAGECHECK_STORE and CHALKOS_IMAGECHECK_SYSTEM name an image's store and system")
	}
	defs, err := facts.Read(filepath.Join(store, strings.TrimPrefix(system, "/nix/store/"), "etc/chalkos/facts.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The system's sw/bin/chalkd links to chalkd's package, which the store holds too.
	checker := filepath.Join(store, strings.TrimPrefix(system, "/nix/store/"), CheckerPath)
	if target, err := os.Readlink(checker); err == nil {
		checker = filepath.Join(store, strings.TrimPrefix(target, "/nix/store/"))
	}
	img := Image{Store: store, System: system, Checker: checker}
	s := DefaultSandbox("/run/chalkd/check")
	for _, tc := range []struct {
		message, refusal string
	}{
		{"hello", ""},
		{"veto", "the config is refused: Demo/one: spec.name: the name is vetoed"},
		{"refuse", "output demo: its check refused the rendered files (exit 1); see the node's journal"},
	} {
		var reached error = errors.New("the checker did not ask for the fact probe")
		f := nodeFacts{facts.NewLoader(context.Background(), defs, facts.Sandbox{Store: store}), s, &reached}
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
		if reached != nil {
			t.Errorf("%s: %v", tc.message, reached)
		}
	}
}
