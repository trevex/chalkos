package layer

import "testing"

func TestCount(t *testing.T) {
	for _, tc := range []struct {
		got, want string
	}{
		{Count(0, "node"), "0 nodes"},
		{Count(1, "node"), "1 node"},
		{Count(uint32(1), "object"), "1 object"},
		{Count(uint64(19), "object"), "19 objects"},
		{Count(1, "OS CA"), "1 OS CA"},
		{Count(2, "OS CA"), "2 OS CAs"},
	} {
		if tc.got != tc.want {
			t.Errorf("%q, want %q", tc.got, tc.want)
		}
	}
}
