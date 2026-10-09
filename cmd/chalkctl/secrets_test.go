package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
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

// only returns the identities given, as the age keys a reader of the secrets file holds.
func only(ids ...age.Identity) func() ([]age.Identity, error) {
	return func() ([]age.Identity, error) { return ids, nil }
}

// TestSecretsEncryptedToTheRecipientsInside checks that gen secrets records the recipients inside
// the encrypted secrets file, that a command changing it encrypts it to those again whatever
// secrets.pub.json says, in place, keeping the previous version and naming the recipients, and
// that --recipient replaces them.
func TestSecretsEncryptedToTheRecipientsInside(t *testing.T) {
	ta := newTestApp(t)
	id, _ := age.GenerateX25519Identity()
	other, _ := age.GenerateX25519Identity()
	writeFile(t, filepath.Join(ta.dir, ".config", "chalkos", "age.key"), id.String()+"\n"+other.String()+"\n")
	dir := filepath.Join(ta.dir, "cluster")
	os.Mkdir(dir, 0o755)
	if err := ta.run(context.Background(), []string{"gen", "secrets", "--recipient", id.Recipient().String(), "--out", dir}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "secrets.age")
	before, _ := os.ReadFile(path)
	generated, err := pki.ReadSecrets(before, only(id))
	if err != nil || !slices.Equal(generated.Recipients, []string{id.Recipient().String()}) {
		t.Fatalf("the secrets file records the recipients %v, %v", generated.Recipients, err)
	}
	publicPath := filepath.Join(dir, "secrets.pub.json")
	pubData, _ := os.ReadFile(publicPath)
	if strings.Contains(string(pubData), "recipients") {
		t.Errorf("secrets.pub.json records recipients:\n%s", pubData)
	}
	// Anyone who can write secrets.pub.json cannot add a recipient of theirs.
	writeFile(t, publicPath, strings.Replace(string(pubData), "{", `{"recipients": ["`+other.Recipient().String()+`"],`, 1))

	ta.stdout.Reset()
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
	read, err := pki.ReadSecrets(after, only(id))
	if err != nil || read.Rotation == nil {
		t.Fatalf("the changed file does not decrypt to the change: %v", err)
	}
	if prev, _ := os.ReadFile(path + ".prev"); !bytes.Equal(prev, before) {
		t.Error("secrets.age.prev is not the previous file")
	}
	if _, err := pki.ReadSecrets(after, only(other)); err == nil {
		t.Error("a recipient from secrets.pub.json decrypts the changed file")
	}
	if !strings.Contains(ta.stdout.String(), id.Recipient().String()) {
		t.Errorf("the write does not name the recipients: %q", ta.stdout.String())
	}

	// --recipient replaces the recipients, inside the file too.
	f, err = ta.openSecrets(context.Background(), secretFlags{path: path}, dir, changeFlags{recipients: stringList{other.Recipient().String()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ta.writeSecretsFile(f, f.secrets); err != nil {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(path)
	if _, err := pki.ReadSecrets(after, only(id)); err == nil {
		t.Error("a replaced recipient still decrypts")
	}
	read, err = pki.ReadSecrets(after, only(other))
	if err != nil || !slices.Equal(read.Recipients, []string{other.Recipient().String()}) {
		t.Errorf("the new recipient: %v, recorded %v", err, read.Recipients)
	}
}

// TestSecretsKeepTheirFormat checks that an armored file stays armored and a plaintext one
// plaintext.
func TestSecretsKeepTheirFormat(t *testing.T) {
	ta := newTestApp(t)
	id, _ := age.GenerateX25519Identity()
	writeFile(t, filepath.Join(ta.dir, ".config", "chalkos", "age.key"), id.String()+"\n")
	secrets := ta.secrets
	secrets.Recipients = []string{id.Recipient().String()}
	armored, err := secrets.EncodeAs(pki.FormatArmoredAge, id.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(ta.dir, "armored")
	writeFile(t, filepath.Join(dir, "secrets.age"), string(armored))
	for _, path := range []string{filepath.Join(dir, "secrets.age"), filepath.Join(ta.dir, "secrets.json")} {
		f, err := ta.openSecrets(context.Background(), secretFlags{path: path}, ta.dir, changeFlags{})
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

// TestChangedSecretsNeedTheirRecipients checks that an encrypted file that records no recipients
// is not changed, whatever secrets.pub.json records, and that standard input needs --out.
func TestChangedSecretsNeedTheirRecipients(t *testing.T) {
	ta := newTestApp(t)
	id, _ := age.GenerateX25519Identity()
	writeFile(t, filepath.Join(ta.dir, ".config", "chalkos", "age.key"), id.String()+"\n")
	encrypted, _ := ta.secrets.Encrypt(id.Recipient())
	path := filepath.Join(ta.dir, "enc", "secrets.age")
	writeFile(t, path, string(encrypted))
	public, _ := json.Marshal(map[string]any{"version": pki.SecretsVersion, "osCA": ta.secrets.Public().OSCA, "recipients": []string{id.Recipient().String()}})
	writeFile(t, filepath.Join(ta.dir, "enc", "secrets.pub.json"), string(public))
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

// TestPlaintextSecretsEncryptedOnlyOnRequest checks that --recipient on a plaintext secrets file
// is refused unless it is written to a new .age file, which is then encrypted and said so, and
// that a .age file is never written in plaintext.
func TestPlaintextSecretsEncryptedOnlyOnRequest(t *testing.T) {
	ta := newTestApp(t)
	id, _ := age.GenerateX25519Identity()
	path := filepath.Join(ta.dir, "secrets.json")
	recipient := stringList{id.Recipient().String()}
	if _, err := ta.openSecrets(context.Background(), secretFlags{path: path}, ta.dir, changeFlags{recipients: recipient}); err == nil || !strings.Contains(err.Error(), ".age") {
		t.Errorf("--recipient on a plaintext file in place: %v, want a refusal naming .age", err)
	}
	if _, err := ta.openSecrets(context.Background(), secretFlags{path: path}, ta.dir, changeFlags{recipients: recipient, out: filepath.Join(ta.dir, "new.json")}); err == nil {
		t.Error("--recipient on a plaintext file written to a .json file is accepted")
	}
	if _, err := ta.openSecrets(context.Background(), secretFlags{path: path}, ta.dir, changeFlags{out: filepath.Join(ta.dir, "plain.age")}); err == nil || !strings.Contains(err.Error(), "--recipient") {
		t.Errorf("a plaintext file to a .age file without recipients: %v, want a refusal naming --recipient", err)
	}

	out := filepath.Join(ta.dir, "secrets.age")
	f, err := ta.openSecrets(context.Background(), secretFlags{path: path}, ta.dir, changeFlags{recipients: recipient, out: out})
	if err != nil {
		t.Fatal(err)
	}
	if err := ta.writeSecretsFile(f, f.secrets); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	if format, _ := pki.DetectFormat(data); format != pki.FormatAge {
		t.Fatalf("%s is written as format %d", out, format)
	}
	if read, err := pki.ReadSecrets(data, only(id)); err != nil || !slices.Equal(read.Recipients, []string(recipient)) {
		t.Errorf("%s: %v, recipients %v", out, err, read.Recipients)
	}
	if !strings.Contains(ta.stdout.String(), "encrypted") {
		t.Errorf("nothing says the secrets file is encrypted from now on: %q", ta.stdout.String())
	}
}

// TestSecretsRefuseAnotherPublicFile checks that the secrets file is not changed in place beside
// a secrets.pub.json of an OS CA it does not hold, and is beside one of an OS CA it accepts.
func TestSecretsRefuseAnotherPublicFile(t *testing.T) {
	ta := newTestApp(t)
	path := filepath.Join(ta.dir, "secrets.json")
	publicPath := filepath.Join(ta.dir, "secrets.pub.json")
	other, _ := pki.GenerateSecrets(time.Now())
	otherPublic, _ := encodePublic(other)
	writeFile(t, publicPath, string(otherPublic))
	if _, err := ta.openSecrets(context.Background(), secretFlags{path: path}, ta.dir, changeFlags{}); err == nil || !strings.Contains(err.Error(), "secrets.pub.json") {
		t.Errorf("another cluster's secrets.pub.json: %v, want a refusal naming it", err)
	}
	if _, err := ta.openSecrets(context.Background(), secretFlags{path: path}, ta.dir, changeFlags{out: filepath.Join(ta.dir, "new.json")}); err != nil {
		t.Errorf("--out leaves secrets.pub.json alone, yet: %v", err)
	}

	// After the switch of an OS CA rotation written with --out, secrets.pub.json still holds the
	// old OS CA, which the secrets accept.
	public, _ := encodePublic(ta.secrets)
	writeFile(t, publicPath, string(public))
	rotated := ta.secrets
	if err := rotated.BeginRotation(pki.RotateOSCA, time.Now()); err != nil {
		t.Fatal(err)
	}
	rotated.Rotation.Applied = true
	if err := rotated.SwitchRotation(time.Now()); err != nil {
		t.Fatal(err)
	}
	data, _ := rotated.Encode()
	writeFile(t, path, string(data))
	if _, err := ta.openSecrets(context.Background(), secretFlags{path: path}, ta.dir, changeFlags{}); err != nil {
		t.Errorf("a secrets.pub.json of the accepted OS CA: %v", err)
	}
}

// TestReplaceFileFollowsLinks checks that a secrets file that is a symbolic link is replaced
// where it points, keeping the link.
func TestReplaceFileFollowsLinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real", "secrets.json")
	writeFile(t, target, "one")
	link := filepath.Join(dir, "secrets.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(link, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "two" {
		t.Errorf("the target holds %q", got)
	}
	if prev, _ := os.ReadFile(target + ".prev"); string(prev) != "one" {
		t.Errorf("the previous version beside the target holds %q", prev)
	}
}

// TestDirectorySyncedAfterWrites checks that the directory of a new or replaced secrets file is
// synced, and that a sync failing once the file is in place is a warning, not a failure.
func TestDirectorySyncedAfterWrites(t *testing.T) {
	ta := newTestApp(t)
	var synced []string
	failing := false
	defer func(orig func(string) error) { syncDir = orig }(syncDir)
	syncDir = func(dir string) error {
		synced = append(synced, dir)
		if failing {
			return errors.New("sync failed")
		}
		return nil
	}
	outDir := filepath.Join(ta.dir, "out")
	os.Mkdir(outDir, 0o755)
	f, err := ta.openSecrets(context.Background(), secretFlags{path: filepath.Join(ta.dir, "secrets.json")}, ta.dir, changeFlags{out: filepath.Join(outDir, "secrets.json")})
	if err != nil {
		t.Fatal(err)
	}
	if err := ta.writeSecretsFile(f, f.secrets); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(synced, outDir) {
		t.Errorf("synced %v, not the directory of the new file", synced)
	}

	failing = true
	f, err = ta.openSecrets(context.Background(), secretFlags{path: filepath.Join(ta.dir, "secrets.json")}, ta.dir, changeFlags{})
	if err != nil {
		t.Fatal(err)
	}
	changed := f.secrets
	if err := changed.BeginRotation(pki.RotateEncryptionKey, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := ta.writeSecretsFile(f, changed); err != nil {
		t.Errorf("a failed sync after the rename failed the write: %v", err)
	}
	if !strings.Contains(ta.stderr.String(), "warning") {
		t.Errorf("no warning: %q", ta.stderr.String())
	}
	data, _ := os.ReadFile(filepath.Join(ta.dir, "secrets.json"))
	if read, err := pki.ReadSecrets(data, only()); err != nil || read.Rotation == nil {
		t.Errorf("the file was not replaced: %v", err)
	}
}
