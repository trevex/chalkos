package chalkctl

import (
	"bytes"
	"testing"
)

// TestImageName names an image by its root hash's first 12 hex digits, then the tag and revision
// it was built from as far as they are known; a dirty revision says so.
func TestImageName(t *testing.T) {
	root := bytes.Repeat([]byte{0x3f, 0xa9}, 16)
	for _, tc := range []struct {
		revision, tag, want string
	}{
		{"", "", "3fa93fa93fa9"},
		{"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", "v1.5.0", "3fa93fa93fa9 (v1.5.0, a1b2c3d4e5f6)"},
		{"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", "", "3fa93fa93fa9 (a1b2c3d4e5f6)"},
		{"", "v1.5.0", "3fa93fa93fa9 (v1.5.0)"},
		{"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678-dirty", "", "3fa93fa93fa9 (a1b2c3d4e5f6-dirty)"},
		{"abc", "", "3fa93fa93fa9 (abc)"},
	} {
		if got := imageName(root, tc.revision, tc.tag); got != tc.want {
			t.Errorf("imageName(%q, %q) = %q, want %q", tc.revision, tc.tag, got, tc.want)
		}
	}
	if got := imageName(nil, "", ""); got != "unknown" {
		t.Errorf("the name of an image of no root hash = %q", got)
	}
}
