package storeslot

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/storage/gpt"
)

// TestSlotLabels names a slot's store by its root hash's first 16 hex digits, and reads that ID
// back from a slot's labels, which retired and foreign slots do not carry.
func TestSlotLabels(t *testing.T) {
	root := bytes.Repeat([]byte{0x3f, 0xa9}, 16)
	if got := LabelID(root); got != "3fa93fa93fa93fa9" {
		t.Fatalf("LabelID = %s", got)
	}
	for _, tc := range []struct {
		verity, data, want string
	}{
		{"store-verity_3fa93fa93fa93fa9", "store_3fa93fa93fa93fa9", "3fa93fa93fa93fa9"},
		{"_empty", "_empty", ""},
		{"store-verity_3fa93fa93fa93fa9", "store_81bd02c4a9e1aa00", ""},
		{"store-verity", "store", ""},
	} {
		s := Slot{Verity: gpt.Partition{Name: tc.verity}, Data: gpt.Partition{Name: tc.data}}
		if got := s.Label(); got != tc.want {
			t.Errorf("the label of %s and %s = %q, want %q", tc.verity, tc.data, got, tc.want)
		}
	}
}

// skipStream counts the bytes Skip passed over without reading them.
type skipStream struct {
	io.Reader
	skipped int64
}

func (s *skipStream) Skip(n int64) error { s.skipped += n; return nil }

// TestSkipPassesOverASkipper passes over the store and hash tree of a stream that can pass over
// bytes without reading them, as a pull does over layers it has not fetched.
func TestSkipPassesOverASkipper(t *testing.T) {
	s := &skipStream{Reader: strings.NewReader("uki")}
	if err := Skip(Image{StoreSize: 3, VeritySize: 2}, s); err != nil || s.skipped != 5 {
		t.Fatalf("Skip = %v, skipped %d", err, s.skipped)
	}
	if rest, _ := io.ReadAll(s); string(rest) != "uki" {
		t.Errorf("the stream goes on with %q", rest)
	}
}
