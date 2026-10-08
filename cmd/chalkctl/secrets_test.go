package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/trevex/chalkos/pkg/pki"
)

func TestReplaceFileKeepsThePreviousVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.json")
	if err := replaceFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".prev"); !os.IsNotExist(err) {
		t.Errorf("a new file has a previous version: %v", err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	for _, next := range []string{"two", "three"} {
		before, _ := os.ReadFile(path)
		if err := replaceFile(path, []byte(next), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); string(got) != next {
			t.Errorf("file holds %q, want %q", got, next)
		}
		if prev, _ := os.ReadFile(path + ".prev"); string(prev) != string(before) {
			t.Errorf("previous version holds %q, want %q", prev, before)
		}
	}
	for _, p := range []string{path, path + ".prev"} {
		if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o640 {
			t.Errorf("%s: mode %v, %v; want the file's own 0640", p, info.Mode(), err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("left behind %v", entries)
	}
}

// TestSecretsEncryptedAgainToRecordedRecipients checks that gen secrets records the recipients
// in secrets.pub.json and that a command changing the secrets file encrypts it to them again, in
// place, keeping the previous version, and that --recipient replaces them.
func TestSecretsEncryptedAgainToRecordedRecipients(t *testing.T) {
	ta := newTestApp(t)
	id, _ := age.GenerateX25519Identity()
	other, _ := age.GenerateX25519Identity()
	writeFile(t, filepath.Join(ta.dir, ".config", "chalkos", "age.key"), id.String()+"\n"+other.String()+"\n")
	dir := filepath.Join(ta.dir, "cluster")
	os.Mkdir(dir, 0o755)
	if err := ta.run(context.Background(), []string{"gen", "secrets", "--recipient", id.Recipient().String(), "--out", dir}); err != nil {
		t.Fatal(err)
	}
	pubData, _ := os.ReadFile(filepath.Join(dir, "secrets.pub.json"))
	public, err := pki.ReadPublic(pubData)
	if err != nil || len(public.Recipients) != 1 || public.Recipients[0] != id.Recipient().String() {
		t.Fatalf("secrets.pub.json records %v, %v", public.Recipients, err)
	}

	path := filepath.Join(dir, "secrets.age")
	before, _ := os.ReadFile(path)
	f, err := ta.openSecrets(context.Background(), secretFlags{path: path}, dir, changeFlags{})
	if err != nil {
		t.Fatal(err)
	}
	changed := f.secrets
	if err := changed.BeginRotation(pki.RotateServiceAccountKey, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := ta.writeSecretsFile(f, changed); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	only := func(ids ...age.Identity) func() ([]age.Identity, error) {
		return func() ([]age.Identity, error) { return ids, nil }
	}
	read, err := pki.ReadSecrets(after, only(id))
	if err != nil || read.Rotation == nil {
		t.Fatalf("the changed file does not decrypt to the change: %v", err)
	}
	if prev, _ := os.ReadFile(path + ".prev"); !bytes.Equal(prev, before) {
		t.Error("secrets.age.prev is not the previous file")
	}
	if _, err := pki.ReadSecrets(after, only(other)); err == nil {
		t.Error("a recipient that was not recorded decrypts the changed file")
	}

	// --recipient replaces the recorded recipients, also in secrets.pub.json.
	f, err = ta.openSecrets(context.Background(), secretFlags{path: path}, dir, changeFlags{recipients: stringList{other.Recipient().String()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ta.writeSecretsFile(f, f.secrets); err != nil {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(path)
	if _, err := pki.ReadSecrets(after, only(other)); err != nil {
		t.Errorf("the new recipient cannot decrypt: %v", err)
	}
	pubData, _ = os.ReadFile(filepath.Join(dir, "secrets.pub.json"))
	if public, _ := pki.ReadPublic(pubData); len(public.Recipients) != 1 || public.Recipients[0] != other.Recipient().String() {
		t.Errorf("secrets.pub.json records %v", public.Recipients)
	}
}

// TestSecretsKeepTheirFormat checks that an armored file stays armored and a plaintext one
// plaintext.
func TestSecretsKeepTheirFormat(t *testing.T) {
	ta := newTestApp(t)
	id, _ := age.GenerateX25519Identity()
	writeFile(t, filepath.Join(ta.dir, ".config", "chalkos", "age.key"), id.String()+"\n")
	armored, err := ta.secrets.EncodeAs(pki.FormatArmoredAge, id.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(ta.dir, "armored")
	writeFile(t, filepath.Join(dir, "secrets.age"), string(armored))
	for _, path := range []string{filepath.Join(dir, "secrets.age"), filepath.Join(ta.dir, "secrets.json")} {
		f, err := ta.openSecrets(context.Background(), secretFlags{path: path}, ta.dir, changeFlags{recipients: stringList{id.Recipient().String()}})
		if err != nil {
			t.Fatal(err)
		}
		want := f.format
		if err := ta.writeSecretsFile(f, f.secrets); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(path)
		if got, _ := pki.DetectFormat(data); got != want {
			t.Errorf("%s: written as %d, read as %d", path, got, want)
		}
	}
}

// TestChangedSecretsNeedTheirRecipients checks that an encrypted file whose recipients nothing
// records is not changed, and that standard input needs --out.
func TestChangedSecretsNeedTheirRecipients(t *testing.T) {
	ta := newTestApp(t)
	id, _ := age.GenerateX25519Identity()
	writeFile(t, filepath.Join(ta.dir, ".config", "chalkos", "age.key"), id.String()+"\n")
	encrypted, _ := ta.secrets.Encrypt(id.Recipient())
	path := filepath.Join(ta.dir, "enc", "secrets.age")
	writeFile(t, path, string(encrypted))
	if _, err := ta.openSecrets(context.Background(), secretFlags{path: path}, ta.dir, changeFlags{}); err == nil || !strings.Contains(err.Error(), "--recipient") {
		t.Errorf("err = %v, want a refusal naming --recipient", err)
	}
	if _, err := ta.openSecrets(context.Background(), secretFlags{path: "-"}, ta.dir, changeFlags{}); err == nil || !strings.Contains(err.Error(), "--out") {
		t.Errorf("err = %v, want a refusal naming --out", err)
	}
	if _, err := ta.openSecrets(context.Background(), secretFlags{path: path}, ta.dir, changeFlags{recipients: stringList{"AGE-SECRET-KEY-1QQQ"}}); err == nil || strings.Contains(err.Error(), "AGE-SECRET") {
		t.Errorf("err = %v, want a refusal that does not echo the key", err)
	}
}
