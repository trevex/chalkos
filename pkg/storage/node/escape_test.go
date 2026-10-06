package node

import "testing"

// Expected values are the output of systemd-escape from systemd 261.
func TestEscape(t *testing.T) {
	for in, want := range map[string]string{
		"my-vol": `my\x2dvol`,
		".x":     `\x2ex`,
		"a b":    `a\x20b`,
		"data":   "data",
	} {
		if got := escapeName(in); got != want {
			t.Errorf("escapeName(%q) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"/srv/plain-data":  `srv-plain\x2ddata`,
		"/var/lib/data/":   "var-lib-data",
		"//a//.b/":         "a-.b",
		"/":                "-",
		"/dev/mapper/swap": "dev-mapper-swap",
	} {
		if got := escapePath(in); got != want {
			t.Errorf("escapePath(%q) = %s, want %s", in, got, want)
		}
	}
}
