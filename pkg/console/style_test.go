package console_test

import (
	"testing"

	"github.com/trevex/chalkos/pkg/console"
)

func TestWrap(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"short\nlines stay", "short\nlines stay"},
		{"one two three four", "one two\n  three\n  four"},
		{"  ab cd ef gh ij", "  ab cd ef\n    gh ij"},
		{"averylongword x", "averylongword\n  x"},
	} {
		if got := console.Wrap(tc.text, 10); got != tc.want {
			t.Errorf("Wrap(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}
