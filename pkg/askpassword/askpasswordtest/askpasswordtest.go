// Package askpasswordtest makes password requests as systemd does, for tests of the agents that
// answer them: an ask.* file in a directory and the datagram socket it names, which records the
// answers.
package askpasswordtest

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Request is a request a test makes. Name names its files, ask.<Name> and sck.<Name>.
type Request struct {
	Name    string
	Message string
	ID      string
	Echo    bool
	Silent  bool
	// NotAfter is the CLOCK_MONOTONIC time in microseconds the request ends at; zero for none.
	NotAfter uint64
	// PID is the asking process; the test's own when zero.
	PID int
	// Since is the file's time; now when zero.
	Since time.Time
}

// Ask writes req into dir and returns the answers its socket receives, each datagram as sent.
func Ask(t *testing.T, dir string, req Request) <-chan string {
	t.Helper()
	socket := filepath.Join(dir, "sck."+req.Name)
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	answers := make(chan string, 4)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, _, err := conn.ReadFromUnix(buf)
			if err != nil {
				return
			}
			answers <- string(buf[:n])
		}
	}()
	pid := req.PID
	if pid == 0 {
		pid = os.Getpid()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Ask]\nPID=%d\nSocket=%s\nAcceptCached=0\nEcho=%d\nNotAfter=%d\nSilent=%d\nMessage=%s\n",
		pid, socket, boolInt(req.Echo), req.NotAfter, boolInt(req.Silent), req.Message)
	if req.ID != "" {
		fmt.Fprintf(&b, "Id=%s\n", req.ID)
	}
	// systemd writes a temporary file and renames it into place.
	tmp := filepath.Join(dir, "tmp."+req.Name)
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if !req.Since.IsZero() {
		if err := os.Chtimes(tmp, req.Since, req.Since); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(tmp, filepath.Join(dir, "ask."+req.Name)); err != nil {
		t.Fatal(err)
	}
	return answers
}

// Remove removes the request's file, as systemd does once it is answered or has ended.
func Remove(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, "ask."+name)); err != nil {
		t.Fatal(err)
	}
}

// Next is the next answer, or fails the test after 20 seconds.
func Next(t *testing.T, answers <-chan string) string {
	t.Helper()
	select {
	case a := <-answers:
		return a
	case <-time.After(20 * time.Second):
		t.Fatal("no answer came")
		return ""
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
