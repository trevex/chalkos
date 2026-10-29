package credprovider

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/config"
)

var logins = []config.RegistryCredentials{
	{Host: "Index.Docker.io", Username: "hub-bot", Password: "hub-s3cret"},
	{Host: "10.0.0.5:5000", Insecure: true},
	{Host: "[fd00::1]:5000", Username: "lan", Password: "lan-s3cret"},
	{Host: "quay.io", Username: "robot"},
}

// TestWrite writes the provider's config, matching the hosts of the Registry documents with a
// user as the kubelet names them, the credentials readable by root alone, and flags naming
// both; without a user, no flags and no credentials.
func TestWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kubernetes", "credential-provider")
	if err := Write(dir, "/nix/store/x-chalkd/bin", logins); err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	data, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"apiVersion": "kubelet.config.k8s.io/v1", "kind": "CredentialProviderConfig",
		"providers": []any{map[string]any{
			"name": "chalkd", "args": []any{"kubernetes", "credential-provider"}, "apiVersion": "credentialprovider.kubelet.k8s.io/v1",
			"defaultCacheDuration": "5m", "matchImages": []any{"[fd00::1]:5000", "docker.io", "quay.io"},
		}},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("config.json = %v, want %v", cfg, want)
	}
	if strings.Contains(string(data), "s3cret") {
		t.Errorf("config.json holds a password: %s", data)
	}
	if fi, err := os.Stat(filepath.Dir(dir)); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("%s: %v, %v", filepath.Dir(dir), fi, err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("%s: %v, %v", dir, fi, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "credentials.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("credentials.json: %v, %v", fi, err)
	}
	flags, _ := os.ReadFile(filepath.Join(dir, "flags"))
	if want := "KUBELET_CREDENTIAL_PROVIDER_ARGS=--image-credential-provider-config=" + filepath.Join(dir, "config.json") + " --image-credential-provider-bin-dir=/nix/store/x-chalkd/bin\n"; string(flags) != want {
		t.Errorf("flags = %q, want %q", flags, want)
	}
	if err := Write(dir, "/bin", logins[1:2]); err != nil {
		t.Fatal(err)
	}
	if flags, _ := os.ReadFile(filepath.Join(dir, "flags")); string(flags) != "KUBELET_CREDENTIAL_PROVIDER_ARGS=\n" {
		t.Errorf("flags without credentials = %q", flags)
	}
	if creds, _ := os.ReadFile(filepath.Join(dir, "credentials.json")); string(creds) != "{}" {
		t.Errorf("credentials.json without credentials = %s", creds)
	}
	if fi, err := os.Stat(filepath.Join(dir, "credentials.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("credentials.json rewritten: %v, %v", fi, err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("%s without credentials: %v, %v", dir, fi, err)
	}
	if cfg, _ := os.ReadFile(filepath.Join(dir, "config.json")); !strings.Contains(string(cfg), `"matchImages":[]`) {
		t.Errorf("config.json without credentials = %s", cfg)
	}
}

// TestWriteTightensTheDirectory makes an existing directory readable by root alone.
func TestWriteTightensTheDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, "/bin", logins); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("%s: %v, %v", dir, fi, err)
	}
}

func provide(t *testing.T, dir, image string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	in := `{"apiVersion":"credentialprovider.kubelet.k8s.io/v1","kind":"CredentialProviderRequest","image":"` + image + `"}`
	err := Provide(dir, strings.NewReader(in), &out)
	return strings.TrimSpace(out.String()), err
}

// TestProvide answers with the credentials of the image's registry, by any of its names, keyed
// as the kubelet matches images, none for another registry or port, and refuses another API
// version.
func TestProvide(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, "/bin", logins); err != nil {
		t.Fatal(err)
	}
	const none = `{"apiVersion":"credentialprovider.kubelet.k8s.io/v1","kind":"CredentialProviderResponse","cacheKeyType":"Registry"}`
	for image, want := range map[string]string{
		"docker.io/library/busybox:1.37.0":                        `{"apiVersion":"credentialprovider.kubelet.k8s.io/v1","kind":"CredentialProviderResponse","cacheKeyType":"Registry","auth":{"docker.io":{"username":"hub-bot","password":"hub-s3cret"}}}`,
		"index.docker.io/library/busybox:1.37.0":                  `{"apiVersion":"credentialprovider.kubelet.k8s.io/v1","kind":"CredentialProviderResponse","cacheKeyType":"Registry","auth":{"docker.io":{"username":"hub-bot","password":"hub-s3cret"}}}`,
		"[fd00::1]:5000/e2e/x@sha256:" + strings.Repeat("ab", 32): `{"apiVersion":"credentialprovider.kubelet.k8s.io/v1","kind":"CredentialProviderResponse","cacheKeyType":"Registry","auth":{"[fd00::1]:5000":{"username":"lan","password":"lan-s3cret"}}}`,
		"[fd00::1]:5001/e2e/x:v1":                                 none,
		"[fd00::1]/e2e/x:v1":                                      none,
		"10.0.0.5:5000/e2e/x:v1":                                  none,
		"ghcr.io/x/y:v1":                                          none,
		"quay.io/x/y:v1":                                          `{"apiVersion":"credentialprovider.kubelet.k8s.io/v1","kind":"CredentialProviderResponse","cacheKeyType":"Registry","auth":{"quay.io":{"username":"robot","password":""}}}`,
		"not a reference":                                         none,
	} {
		if got, err := provide(t, dir, image); err != nil || got != want {
			t.Errorf("%s: %s, %v; want %s", image, got, err, want)
		}
	}
	if err := Provide(dir, strings.NewReader(`{"apiVersion":"credentialprovider.kubelet.k8s.io/v1beta1","kind":"CredentialProviderRequest","image":"ghcr.io/x"}`), &bytes.Buffer{}); err == nil {
		t.Error("another API version was answered")
	}
}

// TestProvideWithoutCredentials answers with none before chalkd wrote any.
func TestProvideWithoutCredentials(t *testing.T) {
	got, err := provide(t, filepath.Join(t.TempDir(), "missing"), "docker.io/library/busybox:1.37.0")
	if want := `{"apiVersion":"credentialprovider.kubelet.k8s.io/v1","kind":"CredentialProviderResponse","cacheKeyType":"Registry"}`; err != nil || got != want {
		t.Errorf("%s, %v; want %s", got, err, want)
	}
}

// TestWriteRefusesIPv6WithoutPort refuses an IPv6 host the kubelet could not match.
func TestWriteRefusesIPv6WithoutPort(t *testing.T) {
	err := Write(t.TempDir(), "/bin", []config.RegistryCredentials{{Host: "[fd00::1]", Username: "lan", Password: "p"}})
	if err == nil || !strings.Contains(err.Error(), "only with its port") {
		t.Errorf("err = %v", err)
	}
	if err := Write(t.TempDir(), "/bin", []config.RegistryCredentials{{Host: "[fd00::1]"}}); err != nil {
		t.Errorf("an IPv6 host without a user: %v", err)
	}
	if err := Write(t.TempDir(), "/bin", []config.RegistryCredentials{{Host: "[fd00::1]:443", Username: "lan", Password: "p"}}); err != nil {
		t.Errorf("an IPv6 host with port 443: %v", err)
	}
}

// TestProvideRefusesCorruptCredentials fails rather than answer from a file it cannot read.
func TestProvideRefusesCorruptCredentials(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := provide(t, dir, "docker.io/library/busybox:1.37.0"); err == nil {
		t.Errorf("answered %s", got)
	}
}
