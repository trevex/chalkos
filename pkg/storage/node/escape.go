package node

import (
	"strings"

	"github.com/trevex/chalkos/pkg/internal/strs"
)

// escapePath escapes an absolute path for use in a unit name, as `systemd-escape --path` does.
func escapePath(p string) string {
	trimmed := strings.Trim(p, "/")
	for strings.Contains(trimmed, "//") {
		trimmed = strings.ReplaceAll(trimmed, "//", "/")
	}
	if trimmed == "" {
		return "-"
	}
	return strs.UnitEscape(trimmed)
}
