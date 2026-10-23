package store

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

// demoSchemas declare the Demo kind, whose token is a secretRef.
func demoSchemas(t *testing.T) *schema.Schemas {
	t.Helper()
	s, err := schema.LoadSchemas("../../../test/fixtures/kinds/image/schemas")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWriteRuntime(t *testing.T) {
	run := t.TempDir()
	units := map[string][]string{"demo": {"Demo"}, "idle": {"Host"}}
	if err := WriteRuntime(run, parse(t, set), demoSchemas(t), units); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(run, "config", "demo.example.org", "Demo", "one.json")); !strings.Contains(got, `"name":"first"`) {
		t.Errorf("Demo = %s", got)
	}
	for _, p := range []string{filepath.Join("chalkos.dev", "Secret"), secretsDir, versionFile} {
		if _, err := os.Stat(filepath.Join(run, "config", p)); !os.IsNotExist(err) {
			t.Errorf("wrote %s below config/", p)
		}
	}
	cred := filepath.Join(run, "credentials", "demo", "demo.token")
	if got := readFile(t, cred); got != "s3cret" {
		t.Errorf("credential = %q", got)
	}
	if m := mode(t, cred); m != 0o600 {
		t.Errorf("credential mode = %v", m)
	}
	if m := mode(t, filepath.Join(run, "credentials", "demo")); m != 0o700 {
		t.Errorf("credential directory mode = %v", m)
	}
	if entries, err := os.ReadDir(filepath.Join(run, "credentials", "idle")); err != nil || len(entries) != 0 {
		t.Errorf("idle's credentials = %v, %v; want an empty directory", entries, err)
	}

	// The Demo no longer references the Secret: its value leaves the unit's directory.
	unreferenced := strings.Replace(set, "token: {secretRef: {name: demo, key: token}}", "token: null", 1)
	if err := WriteRuntime(run, parse(t, unreferenced), demoSchemas(t), units); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cred); !os.IsNotExist(err) {
		t.Error("kept the value of a secret no kind references")
	}
}

func TestWriteRuntimeWithoutConfig(t *testing.T) {
	run := t.TempDir()
	if err := WriteRuntime(run, nil, nil, map[string][]string{"demo": {"Demo"}}); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(run, "config"), filepath.Join(run, "credentials", "demo")} {
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Errorf("%s = %v, %v; want an empty directory", dir, entries, err)
		}
	}
}

func TestWriteRuntimeRefusesAUnitName(t *testing.T) {
	if err := WriteRuntime(t.TempDir(), nil, nil, map[string][]string{"../escape": {"Demo"}}); err == nil {
		t.Error("wrote credentials for the unit ../escape")
	}
}

func TestCheckCredentials(t *testing.T) {
	s := demoSchemas(t)
	if err := CheckCredentials(parse(t, set), s); err != nil {
		t.Errorf("err = %v", err)
	}
	slash := strings.Replace(set, "key: token", "key: a/b", 1)
	if err := CheckCredentials(parse(t, slash), s); err == nil || !strings.Contains(err.Error(), `"a/b"`) {
		t.Errorf("a key with a slash: %v", err)
	}
	// systemd loads each file as the credential secret_<file> and refuses a unit whose
	// credential IDs are not valid file and fd names: printable ASCII without a colon, at most
	// 255 bytes with the prefix.
	for _, key := range []string{"a:b", "schlüssel", "tab\there", strings.Repeat("k", 250)} {
		bad := strings.Replace(set, "key: token", "key: "+strconv.Quote(key), 1)
		if err := CheckCredentials(parse(t, bad), s); err == nil || !strings.Contains(err.Error(), strconv.Quote(key)) {
			t.Errorf("key %q: %v", key, err)
		}
	}
	if err := CheckCredentials(parse(t, strings.Replace(set, "key: token", "key: "+strings.Repeat("k", 243), 1)), s); err != nil {
		t.Errorf("a credential ID of 255 bytes: %v", err)
	}
	clash := `
apiVersion: demo.example.org/v1alpha1
kind: Demo
metadata: {name: one}
spec: {name: one, token: {secretRef: {name: a.b, key: c}}}
---
apiVersion: demo.example.org/v1alpha1
kind: Demo
metadata: {name: two}
spec: {name: two, token: {secretRef: {name: a, key: b.c}}}
`
	if err := CheckCredentials(parse(t, clash), s); err == nil || !strings.Contains(err.Error(), "a.b.c") {
		t.Errorf("two references of one file: %v", err)
	}
}

// Addon documents stay in the stored set, readable by root alone: their objects, Secrets among
// them, are chalkd's to apply, so no file below /run holds them, and one an earlier write left
// is removed.
func TestWriteRuntimeLeavesOutAddons(t *testing.T) {
	run := t.TempDir()
	units := map[string][]string{"demo": {"Demo"}}
	if err := WriteRuntime(run, parse(t, set), demoSchemas(t), units); err != nil {
		t.Fatal(err)
	}
	addons := filepath.Join(run, "config", "kubernetes.chalkos.dev", "Addon")
	if err := os.MkdirAll(addons, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(addons, "old.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withAddon := set + `---
apiVersion: kubernetes.chalkos.dev/v1alpha1
kind: Addon
metadata: {name: manifests}
spec: {objects: [{apiVersion: v1, kind: Secret, metadata: {name: s}, data: {k: dg==}}]}
`
	if err := WriteRuntime(run, parse(t, withAddon), demoSchemas(t), units); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(run, "config", "kubernetes.chalkos.dev")); !os.IsNotExist(err) {
		t.Errorf("wrote Addon documents below config/: %v", err)
	}
	if got := readFile(t, filepath.Join(run, "config", "demo.example.org", "Demo", "one.json")); !strings.Contains(got, `"name":"first"`) {
		t.Errorf("Demo = %s", got)
	}

	stored := filepath.Join(t.TempDir(), "config")
	if err := Write(stored, Stored{Delivered: parse(t, withAddon), Documents: parse(t, withAddon), Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	got, _, err := Read(stored)
	if err != nil || !slices.ContainsFunc(got.Documents, func(d config.Document) bool { return d.Kind == "Addon" }) {
		t.Errorf("the stored set lost its Addon: %v", err)
	}
	for _, tree := range []string{"documents", "delivered"} {
		if m := mode(t, filepath.Join(stored, tree, "kubernetes.chalkos.dev", "Addon")); m != 0o700 {
			t.Errorf("stored Addon directory mode in %s/ = %v", tree, m)
		}
		if m := mode(t, filepath.Join(stored, tree, "kubernetes.chalkos.dev", "Addon", "manifests.json")); m != 0o600 {
			t.Errorf("stored Addon mode in %s/ = %v", tree, m)
		}
	}
}

// TestCredentialSecrets reads a secretRef's value from the file systemd loads it as, and nothing
// without the file or the directory.
func TestCredentialSecrets(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret_hub.token"), []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := CredentialSecrets(dir)
	if v, ok := secret(config.SecretRef{Name: "hub", Key: "token"}); !ok || v != "s3cret" {
		t.Errorf("hub.token = %q, %v", v, ok)
	}
	if _, ok := secret(config.SecretRef{Name: "hub", Key: "nope"}); ok {
		t.Error("a missing file gave a value")
	}
	if _, ok := CredentialSecrets("")(config.SecretRef{Name: "hub", Key: "token"}); ok {
		t.Error("no directory gave a value")
	}
}

// TestCredentialSecretsStaysInTheDirectory gives no value for a reference that would name a file
// outside the directory.
func TestCredentialSecretsStaysInTheDirectory(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "credentials")
	if err := os.MkdirAll(filepath.Join(dir, "secret_hub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "outside.token"), []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, ok := CredentialSecrets(dir)(config.SecretRef{Name: "hub/../../outside", Key: "token"}); ok {
		t.Errorf("a reference with / read %q", v)
	}
}
