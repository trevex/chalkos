package chalkctl

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
)

func TestParseSecretSource(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		input, provider, path, pointer string
	}{
		{"secrets.json", "file", "secrets.json", ""},
		{"secrets.age", "age", "secrets.age", ""},
		{"secrets.enc.yaml", "sops", "secrets.enc.yaml", ""},
		{"secrets.enc.yaml.age", "age", "secrets.enc.yaml.age", ""},
		{"secrets#old", "file", "secrets#old", ""},
		{"file://./secrets.enc.json", "file", "secrets.enc.json", ""},
		{"sops://./secret%23file.yaml#/bgp/password", "sops", "secret#file.yaml", "/bgp/password"},
		{"age://./cipher#/chalkos", "age", "cipher", "/chalkos"},
		{"-", "", "-", ""},
	} {
		source, err := parseSecretSource(tc.input, dir, false)
		path := tc.path
		if path != "-" {
			path = filepath.Join(dir, path)
		}
		if want := (secretSource{tc.provider, path, tc.pointer}); err != nil || source != want {
			t.Errorf("%s = %+v, %v; want %+v", tc.input, source, err, want)
		}
	}
	for _, input := range []string{"secrets.age", "-", "https://host/secrets.age", "file://bad%zz", "file://file#foo"} {
		if _, err := parseSecretSource(input, dir, true); err == nil {
			t.Errorf("accepted %q as an explicit local source", input)
		}
	}
}

func TestValidateSecretSourcesMatchesLiteralConfig(t *testing.T) {
	ta := newTestApp(t)
	dir := filepath.Join(ta.dir, "nodes")
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(ta.dir, "custom.identity")
	writeFile(t, identity, key.String()+"\n")
	var ciphertext bytes.Buffer
	enc, err := age.Encrypt(&ciphertext, key.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write([]byte("bgp: {password: shared-value}\n")); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "service.age"), ciphertext.String())
	writeFile(t, filepath.Join(dir, "plain"), "line with newline\n")
	const secret = "apiVersion: chalkos.dev/v1alpha1\nkind: Secret\nmetadata: {name: bgp}\n"
	validate := func(spec string) string {
		t.Helper()
		inventory := writeInventory(t, ta.dir, n1SystemDisk, n1Network, secret+spec)
		ta.stdout.Reset()
		if err := ta.run(t.Context(), []string{"validate", "--inventory", inventory, "--identity", identity}); err != nil {
			t.Fatal(err)
		}
		return ta.stdout.String()
	}
	fromSources := validate("spec: {data: {literal: keep}, sources: {password: 'age://./service.age#/bgp/password', file: 'file://./plain'}}\n")
	fromLiterals := validate("spec: {data: {literal: keep, password: shared-value, file: \"line with newline\\n\"}}\n")
	if fromSources != fromLiterals || !strings.Contains(fromSources, "n1: valid") {
		t.Fatalf("sources = %q; literals = %q", fromSources, fromLiterals)
	}
	inventory := writeInventory(t, ta.dir, n1SystemDisk, n1Network, secret+"spec: {sources: {password: plain}}\n")
	if err := ta.run(t.Context(), []string{"validate", "--inventory", inventory}); err == nil || !strings.Contains(err.Error(), "must use a provider URI") {
		t.Fatalf("expected URI error, got %v", err)
	}
}

func TestBundleAndServiceSecretShareOneSOPSRead(t *testing.T) {
	ta := newTestApp(t)
	bundle, err := ta.secrets.Encode()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ta.dir, "shared.enc.json")
	writeFile(t, path, `{"chalkos":`+string(bundle)+`,"service":{"p/a~ss":["example"]}}`)
	calls := filepath.Join(ta.dir, "calls")
	t.Setenv("SOPS_TEST_CALLS", calls)
	writeFile(t, filepath.Join(ta.dir, "sops"), "#!/bin/sh\nprintf 'read\\n' >> \"$SOPS_TEST_CALLS\"\ncat \"$2\"\n")
	if err := os.Chmod(filepath.Join(ta.dir, "sops"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", ta.dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	sources := &secretSources{ctx: t.Context(), app: ta.app}
	ctx := context.WithValue(t.Context(), secretSourcesKey{}, sources)
	got, err := ta.loadSecrets(ctx, layer.SecretsFlags{Path: "sops://" + path + "#/chalkos"})
	if err != nil || got.Public().OSCA != ta.secrets.Public().OSCA {
		t.Fatalf("bundle: %v", err)
	}
	value, err := sources.secretValue("sops://./shared.enc.json#/service/p~1a~0ss/0", ta.dir)
	if err != nil || value != "example" {
		t.Fatalf("resolve = %q, %v", value, err)
	}
	log, err := os.ReadFile(calls)
	if err != nil || string(log) != "read\n" {
		t.Fatalf("sops calls = %q, %v", log, err)
	}
	if _, err := ta.openSecrets(ctx, layer.SecretsFlags{Path: path}, changeFlags{}); err == nil || !strings.Contains(err.Error(), "rotation requires a whole") {
		t.Fatalf("rotation accepted a SOPS source: %v", err)
	}
}
