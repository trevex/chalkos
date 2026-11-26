package chalkctl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/pki"
)

// A client file others may read draws a warning, once per command, and the command runs; one of
// its owner alone draws none.
func TestClientFileModeWarning(t *testing.T) {
	ta := newTestApp(t)
	s, _ := installedNode(t, ta, nil)
	addr := ta.startNode(t, s)
	reader := ta.writeConfig(t, "alice", pki.RoleReader, 365*24*time.Hour, time.Now())
	ta.withoutSecrets(t)
	want := "chalkctl: warning: " + reader + " holds a private key and is readable by others (mode 0644); chmod 600 it\n"
	for mode, warns := range map[os.FileMode]bool{0o644: true, 0o600: false} {
		if err := os.Chmod(reader, mode); err != nil {
			t.Fatal(err)
		}
		ta.stderr.Reset()
		if err := ta.run(context.Background(), ta.args([]string{"status", "n1", "--config", reader}, addr)); err != nil {
			t.Fatalf("status with a client file of mode %04o: %v", mode, err)
		}
		if got := strings.Count(ta.stderr.String(), want); got != map[bool]int{true: 1, false: 0}[warns] {
			t.Errorf("mode %04o warned %d times:\n%s", mode, got, ta.stderr.String())
		}
	}
}

// A plaintext secrets file and an age identity others may read draw the warning too.
func TestSecretsFileModeWarning(t *testing.T) {
	ta := newTestApp(t)
	dir := t.TempDir()
	identity := filepath.Join(dir, "age.key")
	ta.stderr.Reset()
	if err := os.WriteFile(identity, []byte("AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQSPWSJT\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	ta.parseIdentity(context.Background(), identity)
	if !strings.Contains(ta.stderr.String(), identity+" holds a private key and is readable by others (mode 0640); chmod 600 it") {
		t.Errorf("identity of mode 0640: %q", ta.stderr.String())
	}
	secrets := filepath.Join(ta.dir, "secrets.json")
	if err := os.Chmod(secrets, 0o644); err != nil {
		t.Fatal(err)
	}
	ta.stderr.Reset()
	if err := ta.run(context.Background(), []string{"recovery-key", "n1", "--inventory", ta.inventory}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stderr.String(), secrets+" holds secrets and is readable by others (mode 0644); chmod 600 it") {
		t.Errorf("secrets file of mode 0644: %q", ta.stderr.String())
	}
}
