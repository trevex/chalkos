// Package strs joins values into the lists messages and flags show, counts nouns, as "2 nodes",
// and escapes unit names.
package strs

import (
	"fmt"
	"strings"
)

// Join joins the String of each of xs, separated by sep.
func Join[T fmt.Stringer](xs []T, sep string) string {
	list := make([]string, len(xs))
	for i, x := range xs {
		list[i] = x.String()
	}
	return strings.Join(list, sep)
}

// And lists names as prose, such as "a, b and c"; it is empty for no names.
func And(names []string) string { return list(names, "and") }

// Or lists names as prose, such as "a, b or c"; it is empty for no names.
func Or(names []string) string { return list(names, "or") }

// list lists names as prose, with conj between the last two.
func list(names []string, conj string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " " + conj + " " + names[len(names)-1]
}

// Count is n and the noun, in the plural unless n is one: "1 node", "2 nodes".
func Count[N ~int | ~uint32 | ~uint64](n N, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// UnitEscape escapes s for a unit name as systemd-escape does: ASCII letters, digits, ':', '_'
// and every '.' after the first byte stay, '/' becomes '-', and every other byte becomes \xNN.
func UnitEscape(s string) string {
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		switch {
		case c == '/':
			b.WriteByte('-')
		case c == '.' && i > 0, c == ':', c == '_',
			'0' <= c && c <= '9', 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, `\x%02x`, c)
		}
	}
	return b.String()
}
