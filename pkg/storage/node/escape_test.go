package node

import "testing"

// Expected values are the output of systemd-escape --path from systemd 261.
func TestEscapePath(t *testing.T) {
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
