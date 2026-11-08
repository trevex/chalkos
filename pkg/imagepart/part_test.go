package imagepart

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestReceive copies exactly the part's size, leaves the rest of the stream unread, and refuses a
// part cut short, a part of another SHA-256 and a failed write.
func TestReceive(t *testing.T) {
	sum := sha256.Sum256([]byte("store"))
	p := Part{Size: 5, SHA256: sum[:]}
	r := strings.NewReader("storehash tree")
	var w bytes.Buffer
	if err := p.Receive(&w, r, "store"); err != nil || w.String() != "store" {
		t.Fatalf("Receive = %v, wrote %q", err, w.String())
	}
	if rest, _ := io.ReadAll(r); string(rest) != "hash tree" {
		t.Errorf("the stream goes on with %q", rest)
	}
	for _, tc := range []struct {
		name, stream string
		w            io.Writer
		want         string
	}{
		{"short", "sto", io.Discard, "receive the store: unexpected EOF"},
		{"empty", "", io.Discard, "receive the store: EOF"},
		{"another SHA-256", "stork", io.Discard, "the store's SHA-256 is"},
		{"a failed write", "store", failWriter{}, "write the store: disk full"},
	} {
		if err := p.Receive(tc.w, strings.NewReader(tc.stream), "store"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Receive = %v, want %q", tc.name, err, tc.want)
		}
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }
