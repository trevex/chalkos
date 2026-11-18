package chalkctl

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/clustersecrets"
)

func TestSOPSRotationPreservesCiphertextBackup(t *testing.T) {
	ta := newTestApp(t)
	bundle := filepath.Join(ta.dir, "secrets.enc.json")
	key := filepath.Join(ta.dir, "sops")
	const original = `{"sops":"original cipher"}`
	writeFile(t, bundle, original)
	t.Setenv("SOPS_TEST_PLAINTEXT", filepath.Join(ta.dir, "secrets.json"))
	t.Setenv("PATH", ta.dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeFile(t, key, `#!/bin/sh
case "$1" in
decrypt) cat "$SOPS_TEST_PLAINTEXT" ;;
set)
  [ "$2" = --value-stdin ] && [ "$#" = 8 ] || exit 2
  cat >/dev/null
  [ -z "$SOPS_TEST_FAIL" ] || exit 1
  printf '%s' '{"sops":"next cipher"}' > "$7"
  ;;
unset) exit 2 ;;
esac
`)
	if err := os.Chmod(key, 0o700); err != nil {
		t.Fatal(err)
	}
	flags := layer.SecretsFlags{Path: bundle}
	if _, err := ta.openSecrets(t.Context(), flags, changeFlags{out: "elsewhere"}); err == nil || !strings.Contains(err.Error(), "in place") {
		t.Fatalf("open with --out = %v", err)
	}
	f, err := ta.openSecrets(t.Context(), flags, changeFlags{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	secrets := f.secrets
	if err := secrets.BeginRotation(clustersecrets.RotateKubernetesCA, time.Now()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_TEST_FAIL", "1")
	if err := ta.writeSecretsFile(f, secrets); err == nil {
		t.Fatal("expected an error updating the encrypted copy")
	}
	current, err := os.ReadFile(bundle)
	if err != nil || string(current) != original {
		t.Fatalf("original changed after failure: %q, %v", current, err)
	}
	if _, err := os.Stat(bundle + ".prev"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected backup after failure: %v", err)
	}
	t.Setenv("SOPS_TEST_FAIL", "")
	if err := ta.writeSecretsFile(f, secrets); err != nil {
		t.Fatal(err)
	}
	current, err = os.ReadFile(bundle)
	if err != nil || string(current) != `{"sops":"next cipher"}` {
		t.Fatalf("expected the staged encrypted file, got %q, %v", current, err)
	}
	previous, err := os.ReadFile(bundle + ".prev")
	if err != nil || string(previous) != original {
		t.Fatalf("expected the original ciphertext as the backup, got %q, %v", previous, err)
	}
}
