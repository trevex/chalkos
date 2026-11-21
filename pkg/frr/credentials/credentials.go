// Package credentials fills the peers' passwords of a rendered frr.conf in from a unit's credentials.
package credentials

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/trevex/chalkos/pkg/atomicfile"
)

// passwordLine is a peer group's password line whose password is the placeholder of a
// credential, as the renderers write it: its head and the credential's name.
var passwordLine = regexp.MustCompile(`^( neighbor peer-[a-z0-9.-]+ password )@(secret_[A-Za-z0-9._-]+)@$`)

// Fill writes out, readable by its owner alone, through a new file that replaces the
// old: the config src, with the placeholder @secret_<secret>.<key>@ of each peer group's password
// line replaced by the credential of that name in the directory credentials, as systemd passes a
// unit's credentials. A placeholder elsewhere stays. FRR takes a password of 1 to 80 printable
// ASCII characters without white space; another, a missing credential, or a placeholder without
// a credentials directory ("") fails and leaves out as it was.
func Fill(src, out, credentials string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	text := string(data)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if text == "" {
		lines = nil
	}
	var b strings.Builder
	for _, line := range lines {
		if m := passwordLine.FindStringSubmatch(line); m != nil {
			value, err := credential(credentials, m[2])
			if err != nil {
				return err
			}
			line = m[1] + value
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return atomicfile.Replace(out, []byte(b.String()), 0o600)
}

// credential is the password the credential name in the directory dir holds.
func credential(dir, name string) (string, error) {
	if dir == "" {
		return "", errors.New("frr.service has no credentials")
	}
	path := filepath.Join(dir, name)
	if fi, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) || err == nil && !fi.Mode().IsRegular() {
		return "", fmt.Errorf("frr.conf names the credential %s, which frr.service does not have", name)
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !validPassword(value) {
		return "", fmt.Errorf("%s: FRR takes a password of 1 to 80 printable characters without white space or line breaks", name)
	}
	return string(value), nil
}

// validPassword reports whether FRR takes p as a peer's password: 1 to 80 bytes of printable
// ASCII other than the space.
func validPassword(p []byte) bool {
	if len(p) < 1 || len(p) > 80 {
		return false
	}
	for _, c := range p {
		if c < '!' || c > '~' {
			return false
		}
	}
	return true
}
