package storage

import "testing"

func TestParseSize(t *testing.T) {
	for in, want := range map[string]uint64{
		"4096": 4096,
		"512M": 512 << 20,
		"2G":   2 << 30,
		"1T":   1 << 40,
	} {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "1.5G", "10 GB", "G", "99999999999999999999P"} {
		if _, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) accepted", in)
		}
	}
}

func TestMatchSize(t *testing.T) {
	const tib = 1 << 40
	for _, c := range []struct {
		comparison string
		size       uint64
		want       bool
	}{
		{">= 1T", tib, true},
		{">= 1T", tib - 1, false},
		{">1T", tib, false},
		{"< 2T", tib, true},
		{"<= 1T", tib, true},
		{"= 1T", tib, true},
		{"1T", tib + 1, false},
	} {
		got, err := matchSize(c.comparison, c.size)
		if err != nil || got != c.want {
			t.Errorf("matchSize(%q, %d) = %v, %v; want %v", c.comparison, c.size, got, err, c.want)
		}
	}
	if _, err := matchSize("about 1T", 1); err == nil {
		t.Error("invalid comparison accepted")
	}
}
