package askpassword_test

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/askpassword"
	"github.com/trevex/chalkos/pkg/askpassword/askpasswordtest"
)

func TestParse(t *testing.T) {
	data := "# a comment\n[Ask]\nPID=42\nSocket=/run/systemd/ask-password/sck.1\nAcceptCached=1\n" +
		"Echo=yes\nSilent=0\nNotAfter=1500000\nMessage=Please enter passphrase for disk data:\n" +
		"Icon=drive-harddisk\nId=cryptsetup:/dev/vdb1\n[Other]\nSocket=/elsewhere\n"
	r, err := askpassword.Parse("/run/systemd/ask-password/ask.1", []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := askpassword.Request{
		Path: "/run/systemd/ask-password/ask.1", PID: 42, Socket: "/run/systemd/ask-password/sck.1",
		AcceptCached: true, Echo: true, NotAfter: 1500000,
		Message: "Please enter passphrase for disk data:", Icon: "drive-harddisk", ID: "cryptsetup:/dev/vdb1",
	}
	if r != want {
		t.Errorf("Parse = %+v, want %+v", r, want)
	}
}

// A request without an absolute socket path is refused.
func TestParseRefuses(t *testing.T) {
	for _, data := range []string{
		"[Ask]\nMessage=no socket\n",
		"[Ask]\nSocket=relative\n",
		"[Other]\nSocket=/s\n",
	} {
		if _, err := askpassword.Parse("ask.x", []byte(data)); err == nil {
			t.Errorf("Parse(%q) succeeded", data)
		}
	}
}

// Pending lists the requests oldest first and leaves out those past NotAfter, but keeps those without an end, those whose
// process is gone, files of other names and files it cannot parse.
func TestPending(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	askpasswordtest.Ask(t, dir, askpasswordtest.Request{Name: "new", Message: "second", Since: base.Add(time.Minute)})
	askpasswordtest.Ask(t, dir, askpasswordtest.Request{Name: "old", Message: "first", Since: base})
	askpasswordtest.Ask(t, dir, askpasswordtest.Request{Name: "late", Message: "expired", NotAfter: 1_000_000})
	askpasswordtest.Ask(t, dir, askpasswordtest.Request{Name: "dead", Message: "gone", PID: 99999})
	askpasswordtest.Ask(t, dir, askpasswordtest.Request{Name: "forever", Message: "third", NotAfter: math.MaxUint64, Since: base.Add(2 * time.Minute)})
	askpasswordtest.Ask(t, dir, askpasswordtest.Request{Name: "far", Message: "fourth", NotAfter: 1 << 62, Since: base.Add(3 * time.Minute)})
	for name, data := range map[string]string{"ask.broken": "[Ask]\n", "other": "[Ask]\nSocket=/s\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a := askpassword.Agent{
		Dir:   dir,
		Now:   func() time.Duration { return 2 * time.Second },
		Alive: func(pid int) bool { return pid != 99999 },
	}
	reqs, err := a.Pending()
	if err != nil {
		t.Fatal(err)
	}
	var messages []string
	for _, r := range reqs {
		messages = append(messages, r.Message)
	}
	if !slices.Equal(messages, []string{"first", "second", "third", "fourth"}) {
		t.Errorf("Pending = %q, want first, second, third, fourth", messages)
	}
}

// A directory that does not exist holds no requests.
func TestPendingWithoutDirectory(t *testing.T) {
	reqs, err := askpassword.Agent{Dir: filepath.Join(t.TempDir(), "none")}.Pending()
	if err != nil || len(reqs) != 0 {
		t.Errorf("Pending = %v, %v; want none", reqs, err)
	}
}

// Answer sends + and the password as one datagram, Cancel a lone -.
func TestAnswerAndCancel(t *testing.T) {
	dir := t.TempDir()
	answers := askpasswordtest.Ask(t, dir, askpasswordtest.Request{Name: "1", Message: "m"})
	reqs, err := askpassword.Agent{Dir: dir}.Pending()
	if err != nil || len(reqs) != 1 {
		t.Fatalf("Pending = %v, %v", reqs, err)
	}
	password := []byte("s3cret word")
	if err := reqs[0].Answer(password); err != nil {
		t.Fatal(err)
	}
	if got := askpasswordtest.Next(t, answers); got != "+s3cret word" {
		t.Errorf("answer %q", got)
	}
	if string(password) != "s3cret word" {
		t.Errorf("Answer changed the caller's password to %q", password)
	}
	if err := reqs[0].Cancel(); err != nil {
		t.Fatal(err)
	}
	if got := askpasswordtest.Next(t, answers); got != "-" {
		t.Errorf("cancel %q", got)
	}
}

// An answer to a socket that is gone fails.
func TestAnswerWithoutSocket(t *testing.T) {
	r := askpassword.Request{Socket: filepath.Join(t.TempDir(), "sck.none")}
	if err := r.Answer([]byte("x")); err == nil {
		t.Error("Answer succeeded")
	}
}

// Watch signals when a request comes and when one goes, and closes its channel with ctx.
func TestWatch(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	changes, err := askpassword.Agent{Dir: dir}.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wait := func(what string) {
		t.Helper()
		select {
		case <-changes:
		case <-time.After(10 * time.Second):
			t.Fatalf("no change after %s", what)
		}
	}
	askpasswordtest.Ask(t, dir, askpasswordtest.Request{Name: "1", Message: "m"})
	wait("a request came")
	// The write and the rename may signal twice; the removal must signal on its own.
	time.Sleep(100 * time.Millisecond)
	select {
	case <-changes:
	default:
	}
	askpasswordtest.Remove(t, dir, "1")
	wait("a request went")
	cancel()
	for range changes {
	}
}

// Watching a directory that does not exist fails, so the caller falls back to reading.
func TestWatchWithoutDirectory(t *testing.T) {
	if _, err := (askpassword.Agent{Dir: filepath.Join(t.TempDir(), "none")}).Watch(context.Background()); err == nil {
		t.Error("Watch succeeded")
	}
}

// A value that does not parse leaves its field at the default, as systemd's agents read it.
func TestParseRelaxed(t *testing.T) {
	r, err := askpassword.Parse("ask.x", []byte("[Ask]\nSocket=/s\nPID=x\nEcho=maybe\nNotAfter=-1\nMessage=m\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := (askpassword.Request{Path: "ask.x", Socket: "/s", Message: "m"}); r != want {
		t.Errorf("Parse = %+v, want %+v", r, want)
	}
}

// NotAfter of zero or of systemd's infinity, USEC_INFINITY, never ends; one past what the clock
// can reach ends never either.
func TestEnded(t *testing.T) {
	for _, c := range []struct {
		notAfter uint64
		now      time.Duration
		ended    bool
	}{
		{0, time.Hour, false},
		{math.MaxUint64, math.MaxInt64, false},
		{math.MaxUint64 - 1, math.MaxInt64, false},
		{1 << 62, time.Hour, false},
		{1_000_000, time.Second, false},
		{1_000_000, time.Second + time.Microsecond, true},
	} {
		if got := (askpassword.Request{NotAfter: c.notAfter}).Ended(c.now); got != c.ended {
			t.Errorf("NotAfter %d at %v: Ended = %v, want %v", c.notAfter, c.now, got, c.ended)
		}
	}
}

// A password the asking process would drop as too long is not sent.
func TestAnswerTooLong(t *testing.T) {
	dir := t.TempDir()
	answers := askpasswordtest.Ask(t, dir, askpasswordtest.Request{Name: "1", Message: "m"})
	r := askpassword.Request{Socket: filepath.Join(dir, "sck.1")}
	if err := r.Answer(make([]byte, askpassword.MaxPassword+1)); err == nil {
		t.Error("Answer of a password too long succeeded")
	}
	if err := r.Answer([]byte(strings.Repeat("x", askpassword.MaxPassword))); err != nil {
		t.Fatal(err)
	}
	if got := askpasswordtest.Next(t, answers); len(got) != 1+askpassword.MaxPassword {
		t.Errorf("answer of %d bytes", len(got))
	}
}

// A watch ends when its directory goes, so the caller watches the directory made anew.
func TestWatchEndsWhenTheDirectoryGoes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ask-password")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	changes, err := askpassword.Agent{Dir: dir}.Watch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case _, ok := <-changes:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("the watch did not end")
		}
	}
}
