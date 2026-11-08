package imagepart

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestReceiveChecksTheSize refuses a negative size, and a part of no recorded SHA-256 whatever its
// size.
func TestReceiveChecksTheSize(t *testing.T) {
	for _, tc := range []struct {
		name   string
		p      Part
		stream string
		want   string
	}{
		{"a negative size", Part{Size: -1}, "", "the store has a negative size -1"},
		{"no bytes and no SHA-256", Part{}, "", "the store's SHA-256 is"},
		{"bytes and no SHA-256", Part{Size: 5}, "store", "the store's SHA-256 is"},
	} {
		if err := tc.p.Receive(io.Discard, strings.NewReader(tc.stream), "store"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Receive = %v, want %q", tc.name, err, tc.want)
		}
	}
}

// TestReceiveLargerThanAChunk receives a part one byte larger than what is read at once.
func TestReceiveLargerThanAChunk(t *testing.T) {
	data := bytes.Repeat([]byte{7}, chunk+1)
	sum := sha256.Sum256(data)
	var w bytes.Buffer
	if err := (Part{Size: int64(len(data)), SHA256: sum[:]}).Receive(&w, bytes.NewReader(data), "store"); err != nil || !bytes.Equal(w.Bytes(), data) {
		t.Fatalf("Receive = %v, wrote %d of %d bytes", err, w.Len(), len(data))
	}
}

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
