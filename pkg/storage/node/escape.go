package node

import (
	"fmt"
	"strings"
)

// escapeName escapes a string for use in a unit name, as `systemd-escape` does.
func escapeName(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '/':
			b.WriteByte('-')
		case c == '.' && i == 0, !isUnitNameChar(c):
			fmt.Fprintf(&b, `\x%02x`, c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// escapePath escapes an absolute path for use in a unit name, as `systemd-escape --path` does.
func escapePath(p string) string {
	trimmed := strings.Trim(p, "/")
	for strings.Contains(trimmed, "//") {
		trimmed = strings.ReplaceAll(trimmed, "//", "/")
	}
	if trimmed == "" {
		return "-"
	}
	return escapeName(trimmed)
}

func isUnitNameChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ':' || c == '_' || c == '.'
}
