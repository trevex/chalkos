package modulepolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fakeIPE lays out the securityfs files Load writes and reads, as the kernel shows them once a
// policy named chalkos of version 0.4.0 is loaded.
func fakeIPE(t *testing.T) Paths {
	t.Helper()
	dir := t.TempDir()
	p := Paths{
		Policy:     filepath.Join(dir, "initrd", PolicyFile),
		SecurityFS: filepath.Join(dir, "security"),
		Record:     filepath.Join(dir, "run", RecordFile),
	}
	for name, content := range map[string]string{
		"ipe/new_policy":               "",
		"ipe/policies/chalkos/active":  "0",
		"ipe/policies/chalkos/version": "0.4.0\n",
	} {
		write(t, filepath.Join(p.SecurityFS, name), content)
	}
	return p
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLoadSigned(t *testing.T) {
	p := fakeIPE(t)
	der := []byte{0x30, 0x03, 0x02, 0x01, 0x05}
	write(t, p.Policy, string(padded(der)))
	got := Load(p)
	sum := sha256.Sum256(der)
	want := Record{State: StateLoaded, Version: "0.4.0", Digest: hex.EncodeToString(sum[:])}
	if got != want {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
	if read(t, filepath.Join(p.SecurityFS, "ipe/new_policy")) != string(der) {
		t.Error("new_policy did not get the DER blob alone")
	}
	if read(t, filepath.Join(p.SecurityFS, "ipe/policies/chalkos/active")) != "1" {
		t.Error("the policy was not activated")
	}
	var recorded Record
	if err := json.Unmarshal([]byte(read(t, p.Record)), &recorded); err != nil || recorded != want {
		t.Errorf("record %+v, %v", recorded, err)
	}
}

func TestLoadUnsigned(t *testing.T) {
	p := fakeIPE(t)
	write(t, p.Policy, string(padded([]byte(text))))
	if got := Load(p); got.State != StateUnsigned {
		t.Errorf("Load = %+v", got)
	}
	if read(t, filepath.Join(p.SecurityFS, "ipe/new_policy")) != "" {
		t.Error("an unsigned policy reached the kernel")
	}
}

func TestLoadFailures(t *testing.T) {
	p := fakeIPE(t)
	if got := Load(p); got.State != StateFailed || got.Error == "" {
		t.Errorf("no policy file: %+v", got)
	}
	write(t, p.Policy, string(padded([]byte{0x30, 0x00})))
	// The kernel refuses the policy: new_policy cannot be opened.
	if err := os.Remove(filepath.Join(p.SecurityFS, "ipe/new_policy")); err != nil {
		t.Fatal(err)
	}
	if got := Load(p); got.State != StateFailed || got.Error == "" {
		t.Errorf("refused write: %+v", got)
	}
}
