package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/trevex/chalkos/pkg/manifest"
	"github.com/trevex/chalkos/pkg/pki"
)

func TestRunSignRequiresAllFlags(t *testing.T) {
	err := runSign([]string{"--image", "disk.raw"})
	if err == nil || !strings.Contains(err.Error(), "--repart-json") {
		t.Fatalf("err = %v, want a missing --repart-json error", err)
	}
}

const testManifest = `{
  "schemaVersion": 0,
  "cluster": {"name": "lab", "endpoint": "https://10.0.0.10:6443"},
  "roles": {"test": {"image": "roles.test.image"}},
  "nodes": {
    "n1": {
      "role": "test",
      "identity": {
        "hostname": "n1",
        "network": {"networks": {"10-uplink": {"address": ["10.0.0.11/24"]}}},
        "networkUnits": {},
        "labels": {},
        "taints": [],
        "storage": {
          "disks": {"system": {"ref": {"serial": "chalk-target"}, "seed": "2869f04c-5655-50f4-28b9-6b2eb9700a02", "repart": {"50-var.conf": "[Partition]\nLabel=var\n"}}},
          "volumes": {"var": {"disk": "system", "label": "var", "format": "ext4", "mountPoint": "/var", "encryption": "tpm2", "size": null}},
          "fallback": "recovery-key",
          "encryption": "tpm2"
        },
        "extensions": {}
      }
    }
  }
}`

// testApp is chalkctl with a manifest and a plaintext secrets file in a temporary directory.
type testApp struct {
	*app
	dir      string
	secrets  pki.Secrets
	stdout   *bytes.Buffer
	stderr   *bytes.Buffer
	manifest *manifest.Manifest
}

func newTestApp(t *testing.T) *testApp {
	t.Helper()
	dir := t.TempDir()
	secrets, err := pki.GenerateSecrets(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := secrets.Encode()
	writeFile(t, filepath.Join(dir, "secrets.json"), string(data))
	writeFile(t, filepath.Join(dir, "manifest.json"), testManifest)
	m, err := manifest.Decode(strings.NewReader(testManifest))
	if err != nil {
		t.Fatal(err)
	}
	ta := &testApp{dir: dir, secrets: secrets, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, manifest: m}
	ta.app = &app{
		stdin:  strings.NewReader(""),
		stdout: ta.stdout,
		stderr: ta.stderr,
		nix:    func(context.Context, ...string) ([]byte, error) { return nil, errors.New("nix is not available") },
		home:   dir,
	}
	return ta
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGenSecretsPlaintext(t *testing.T) {
	ta := newTestApp(t)
	out := filepath.Join(ta.dir, "new")
	os.Mkdir(out, 0o755)
	if err := ta.run(context.Background(), []string{"gen", "secrets", "--plaintext", "--out", out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stderr.String(), ".gitignore") {
		t.Errorf("no warning about the unencrypted file: %q", ta.stderr)
	}
	data, err := os.ReadFile(filepath.Join(out, "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pki.ReadSecrets(data, nil); err != nil {
		t.Errorf("the secrets file does not read back: %v", err)
	}
	if info, _ := os.Stat(filepath.Join(out, "secrets.json")); info.Mode().Perm() != 0o600 {
		t.Errorf("secrets.json mode = %v", info.Mode())
	}
	var pub pki.Public
	pubData, _ := os.ReadFile(filepath.Join(out, "secrets.pub.json"))
	if err := json.Unmarshal(pubData, &pub); err != nil || pub.OSCA.Certificate == "" || pub.OSCA.Key != "" || bytes.Contains(pubData, []byte("PRIVATE")) {
		t.Errorf("secrets.pub.json = %s, %v", pubData, err)
	}
	err = ta.run(context.Background(), []string{"gen", "secrets", "--plaintext", "--out", out})
	if err == nil || !strings.Contains(err.Error(), "exists") {
		t.Errorf("err = %v, want a refusal to overwrite the secrets", err)
	}
	if err := ta.run(context.Background(), []string{"gen", "secrets", "--out", out}); err == nil {
		t.Error("generated secrets without recipients or --plaintext")
	}
}

func TestGenSecretsAgeAndRecoveryKey(t *testing.T) {
	ta := newTestApp(t)
	id, _ := age.GenerateX25519Identity()
	writeFile(t, filepath.Join(ta.dir, ".config", "chalkos", "age.key"), id.String()+"\n")
	out := filepath.Join(ta.dir, "cluster")
	os.Mkdir(out, 0o755)
	if err := ta.run(context.Background(), []string{"gen", "secrets", "--recipient", id.Recipient().String(), "--out", out}); err != nil {
		t.Fatal(err)
	}
	encrypted, err := os.ReadFile(filepath.Join(out, "secrets.age"))
	if err != nil || !bytes.HasPrefix(encrypted, []byte("age-encryption.org/")) {
		t.Fatalf("secrets.age = %.20q, %v", encrypted, err)
	}

	// The default secrets file next to the flake, decrypted with the default identity.
	writeFile(t, filepath.Join(out, "manifest.json"), testManifest)
	ta.stdout.Reset()
	if err := ta.run(context.Background(), []string{"recovery-key", "n1", "--flake", out, "--manifest", filepath.Join(out, "manifest.json")}); err != nil {
		t.Fatal(err)
	}
	secrets, _ := pki.ReadSecrets(encrypted, func() ([]age.Identity, error) { return []age.Identity{id}, nil })
	want, _ := pki.RecoveryKey(secrets.RecoverySecret, "lab", "n1")
	if got := strings.TrimSpace(ta.stdout.String()); got != want {
		t.Errorf("recovery key = %q, want %q", got, want)
	}

	// The same file from standard input, as a secret manager would supply it.
	ta.stdout.Reset()
	ta.stdin = bytes.NewReader(encrypted)
	if err := ta.run(context.Background(), []string{"recovery-key", "n1", "--secrets", "-", "--manifest", filepath.Join(out, "manifest.json")}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(ta.stdout.String()); got != want {
		t.Errorf("recovery key from stdin = %q, want %q", got, want)
	}
}

func TestLoadClusterEvaluatesFlake(t *testing.T) {
	ta := newTestApp(t)
	var calls []string
	ta.nix = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch {
		case strings.HasSuffix(args[2], "#chalkos"):
			return []byte(`["lab"]`), nil
		case strings.HasSuffix(args[2], "#chalkos.lab.manifest"):
			return []byte(testManifest), nil
		case args[0] == "build":
			return []byte("/nix/store/x-chalkos\n"), nil
		}
		return nil, errors.New("unexpected")
	}
	c, err := ta.loadCluster(context.Background(), clusterFlags{flake: "/src/lab"})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := ta.buildImage(context.Background(), c, "test")
	if err != nil || dir != "/nix/store/x-chalkos" {
		t.Fatalf("image = %q, %v", dir, err)
	}
	want := []string{
		"eval --json /src/lab#chalkos --apply builtins.attrNames",
		"eval --json /src/lab#chalkos.lab.manifest",
		"build --no-link --print-out-paths /src/lab#chalkos.lab.roles.test.image",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("nix calls = %v, want %v", calls, want)
	}
}

func TestEndpointNeedsAnAddress(t *testing.T) {
	if _, err := endpoint("", "n2", manifest.Identity{}); err == nil || !strings.Contains(err.Error(), "--endpoint") {
		t.Errorf("err = %v", err)
	}
	if got, _ := endpoint("", "n1", manifest.Identity{Network: map[string]any{"networks": map[string]any{
		"20-b": map[string]any{"address": []any{"10.0.1.5/24"}},
		"10-a": map[string]any{"address": []any{"10.0.0.11/24"}},
	}}}); got != "10.0.0.11" {
		t.Errorf("endpoint = %q, want the first network's address", got)
	}
}
