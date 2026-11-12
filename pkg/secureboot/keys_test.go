package secureboot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestNewKeys writes PK, KEK and db for their owner alone and reads them back, and refuses a
// directory that holds files already.
func TestNewKeys(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	if err := NewKeys(dir, "lab"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the keys' directory: %v, %v", info.Mode(), err)
	}
	k, err := LoadKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]*KeyPair{"PK": &k.PK, "KEK": &k.KEK, "db": &k.DB} {
		if got := c.Cert.Subject.CommonName; got != "lab Secure Boot "+name {
			t.Errorf("%s: common name %q", name, got)
		}
		if c.Key.N.BitLen() != 2048 || !c.Key.PublicKey.Equal(c.Cert.PublicKey) {
			t.Errorf("%s: a key of %d bits, matching its certificate: %v", name, c.Key.N.BitLen(), c.Key.PublicKey.Equal(c.Cert.PublicKey))
		}
		if c.Cert.NotAfter.Before(time.Now().AddDate(19, 0, 0)) {
			t.Errorf("%s: valid until %v", name, c.Cert.NotAfter)
		}
		info, err := os.Stat(filepath.Join(dir, name+".key"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s.key: %v, %v; want mode 0600", name, info.Mode(), err)
		}
	}
	if k.Owner == ([16]byte{}) {
		t.Error("no owner GUID")
	}
	owner, err := os.ReadFile(filepath.Join(dir, "owner.txt"))
	if err != nil || strings.TrimSpace(string(owner)) != GUIDString(k.Owner) {
		t.Errorf("owner.txt = %q, %v; want %s", owner, err, GUIDString(k.Owner))
	}
	if err := NewKeys(dir, "lab"); err == nil || !strings.Contains(err.Error(), "is not empty") {
		t.Errorf("NewKeys into a directory that holds keys = %v", err)
	}

	// An empty directory that exists takes the keys, and becomes its owner's alone.
	empty := t.TempDir()
	if err := os.Chmod(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := NewKeys(empty, "lab"); err != nil {
		t.Errorf("NewKeys into an empty directory: %v", err)
	}
	if info, err := os.Stat(empty); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the existing directory: %v, %v; want mode 0700", info.Mode(), err)
	}
}

// TestLoadEnrolmentKeys reads PK and KEK with their keys and db without its key, which stays
// with whoever signs images.
func TestLoadEnrolmentKeys(t *testing.T) {
	dir := t.TempDir()
	if err := NewKeys(dir, "lab"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "db.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeys(dir); err == nil {
		t.Error("LoadKeys read keys without db.key")
	}
	k, err := LoadEnrolmentKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	if k.PK.Key == nil || k.KEK.Key == nil || k.DB.Key != nil || k.DB.Cert.Subject.CommonName != "lab Secure Boot db" {
		t.Errorf("LoadEnrolmentKeys = %+v", k)
	}
	if err := os.Remove(filepath.Join(dir, "KEK.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEnrolmentKeys(dir); err == nil || !strings.Contains(err.Error(), "KEK.key") {
		t.Errorf("LoadEnrolmentKeys without KEK.key = %v", err)
	}
}

// TestLoadKeysNamesTheFile names the file it cannot read.
func TestLoadKeysNamesTheFile(t *testing.T) {
	dir := t.TempDir()
	if err := NewKeys(dir, "lab"); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"KEK.key", "db.crt", "owner.txt"} {
		t.Run(file, func(t *testing.T) {
			copied := t.TempDir()
			if err := os.CopyFS(copied, os.DirFS(dir)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(copied, file), []byte("garbage"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadKeys(copied); err == nil || !strings.Contains(err.Error(), file) {
				t.Errorf("LoadKeys with a damaged %s = %v", file, err)
			}
		})
	}
}

func TestParseGUID(t *testing.T) {
	g, err := ParseGUID("8be4df61-93ca-11d2-aa0d-00e098032b8c")
	if err != nil {
		t.Fatal(err)
	}
	if g != GlobalVariable || GUIDString(g) != GlobalVendor {
		t.Errorf("ParseGUID = %x", g)
	}
	for _, bad := range []string{"", "8be4df61-93ca-11d2-aa0d-00e098032b8", "8be4df61x93ca-11d2-aa0d-00e098032b8c", "zbe4df61-93ca-11d2-aa0d-00e098032b8c"} {
		if _, err := ParseGUID(bad); err == nil {
			t.Errorf("ParseGUID(%q) succeeded", bad)
		}
	}
}
