package credentials

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// credentialsConfig holds a peer group's password placeholder, which the fill replaces, and
// placeholders the renderers never write outside one, which stay.
const credentialsConfig = "frr defaults traditional\n" +
	"router bgp 65001\n" +
	" neighbor peer-fabric password @secret_bgp.fabric@\n" +
	" neighbor peer-fabric description x password @secret_bgp.fabric@\n" +
	" neighbor 203.0.113.254 password @secret_bgp.fabric@\n" +
	"exit\n"

// fillDirs returns the config's source, the filled config's path and the credentials directory.
func fillDirs(t *testing.T, config string) (src, out, creds string) {
	t.Helper()
	dir := t.TempDir()
	src, out, creds = filepath.Join(dir, "rendered.conf"), filepath.Join(dir, "run", "frr.conf"), filepath.Join(dir, "credentials")
	for _, d := range []string{filepath.Dir(out), creds} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(src, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return src, out, creds
}

func setPassword(t *testing.T, creds, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(creds, "secret_bgp.fabric"), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

// read returns path's content and fails unless out's directory holds the filled config alone.
func read(t *testing.T, path string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the fill left %d files, want the filled config alone", len(entries))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The fill replaces the peer group's placeholder alone, writes the config for its owner alone
// and leaves no temporary file.
func TestFill(t *testing.T) {
	src, out, creds := fillDirs(t, credentialsConfig)
	setPassword(t, creds, "example-password")
	if err := Fill(src, out, creds); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(credentialsConfig, "password @secret_bgp.fabric@\n", "password example-password\n", 1)
	if got := read(t, out); got != want {
		t.Errorf("filled config:\n%s\nwant:\n%s", got, want)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("filled config has mode %o, want 600", fi.Mode().Perm())
	}
}

// A config without a trailing line break is filled with one, as each line is.
func TestFillEndsLines(t *testing.T) {
	src, out, _ := fillDirs(t, "frr defaults traditional\n\nexit")
	if err := Fill(src, out, ""); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, out), "frr defaults traditional\n\nexit\n"; got != want {
		t.Errorf("filled config %q, want %q", got, want)
	}
}

// A password FRR would not take, a missing credential, and a placeholder without a credentials
// directory fail the fill and leave the filled config as it was.
func TestFillRefuses(t *testing.T) {
	src, out, creds := fillDirs(t, credentialsConfig)
	setPassword(t, creds, "example-password")
	if err := Fill(src, out, creds); err != nil {
		t.Fatal(err)
	}
	good := read(t, out)
	refused := "FRR takes a password of 1 to 80 printable characters"
	for name, c := range map[string]struct{ password, want string }{
		"two lines":     {"two\nlines", refused},
		"trailing":      {"trailing\n", refused},
		"white space":   {"two words", refused},
		"tab":           {"tab\there", refused},
		"81 characters": {strings.Repeat("x", 81), refused},
		"empty":         {"", refused},
		"unicode":       {"ünïcode", refused},
		"NUL":           {"nul\x00byte", refused},
	} {
		t.Run(name, func(t *testing.T) {
			setPassword(t, creds, c.password)
			if err := Fill(src, out, creds); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("fill: %v, want an error with %q", err, c.want)
			}
			if got := read(t, out); got != good {
				t.Errorf("a refused password changed the filled config:\n%s", got)
			}
		})
	}
	setPassword(t, creds, strings.Repeat("x", 80))
	if err := Fill(src, out, creds); err != nil {
		t.Errorf("80 characters: %v", err)
	}
	setPassword(t, creds, "example-password")
	if err := Fill(src, out, creds); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(creds, "secret_bgp.fabric")); err != nil {
		t.Fatal(err)
	}
	if err := Fill(src, out, creds); err == nil || !strings.Contains(err.Error(), "names the credential secret_bgp.fabric, which frr.service does not have") {
		t.Errorf("missing credential: %v", err)
	}
	if err := Fill(src, out, ""); err == nil || !strings.Contains(err.Error(), "frr.service has no credentials") {
		t.Errorf("no credentials directory: %v", err)
	}
	if got := read(t, out); got != good {
		t.Errorf("a failed fill changed the filled config:\n%s", got)
	}
}
