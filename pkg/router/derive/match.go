package derive

import (
	"math"
	"path"
	"slices"
	"strings"

	"github.com/trevex/chalkos/pkg/refusals"
)

// The Name= of a Network's [Match] sections, as networkd reads it, and the globs the model
// compares: fnmatch globs of Name=, and the names or prefixes and * of the router kinds'
// interfaces, both as path.Match takes them.

// matchLinks are the interface names in the [Match] sections' Name= of a Network document's
// spec, as parseNameMatch reads it: the words that are neither globs nor negated. A Name= that
// parseNameMatch refuses names the links of the words before the one it refuses.
func matchLinks(spec map[string]any) []string {
	m, _ := parseNameMatch("", spec)
	var links []string
	for _, w := range m.Words {
		if !w.Negated && isLiteral(w.Glob) && InterfaceName.MatchString(w.Glob) {
			links = append(links, w.Glob)
		}
	}
	return links
}

// NameMatch is the Name= of a Network document's [Match] sections as systemd-networkd reads it.
// It matches a link that no negated word matches and, when there is a word that is not negated,
// that one of those matches; without words it matches every link.
type NameMatch struct {
	Words []NameWord
}

// NameWord is a word of Name=: Glob is the word as path.Match takes it, Text as Name= gives it,
// and Path the field of the Network document's value that holds it, such as spec.match.name or
// spec.match[1].name[0], which a refusal of the word names.
type NameWord struct {
	Glob, Text, Path string
	Negated          bool
}

// parseNameMatch reads the Name= of the [Match] sections of the Network document network's spec
// as systemd does: the values in order, each split into words at spaces, tabs and line breaks, a
// value of only those dropping the words before it. A value whose first word starts with !
// negates all of its words, and in another value a word that starts with ! negates itself.
// systemd drops a word that is no interface name, and so does parseNameMatch. It refuses a
// backslash, which systemd reads as an escape when it splits the value, and a word it cannot
// match as fnmatch does. It reads interface names alone: networkd also matches a word against a
// link's alternative names, which the documents do not give.
func parseNameMatch(network string, spec map[string]any) (NameMatch, error) {
	var m NameMatch
	for si, s := range OneOrMany(spec["match"]) {
		section, _ := s.(map[string]any)
		names := ItemPath("spec.match", spec["match"], si) + ".name"
		for vi, v := range OneOrMany(section["name"]) {
			at := ItemPath(names, section["name"], vi)
			value, err := UnitValue(v)
			if err != nil {
				continue
			}
			words := strings.FieldsFunc(value, func(r rune) bool { return strings.ContainsRune(UnitWhitespace, r) })
			if len(words) == 0 {
				m.Words = nil
				continue
			}
			inverted := strings.HasPrefix(words[0], "!")
			if inverted {
				words[0] = words[0][1:]
			}
			for _, w := range words {
				if strings.Contains(w, `\`) {
					return m, refusals.Errorf("Network", network, at, "Name= holds %q, whose backslash networkd reads as an escape; name the links without one", w)
				}
				if !systemdIfname(w) {
					continue
				}
				word := NameWord{Text: w, Path: at, Negated: inverted}
				if !inverted {
					w, word.Negated = strings.CutPrefix(w, "!")
				}
				word.Glob = goGlob(w)
				if _, err := path.Match(word.Glob, ""); err != nil || bracketHolds(w, '[') {
					return m, refusals.Errorf("Network", network, at, "Name= holds %q, which is no glob the renderer can match as networkd does", word.Text)
				}
				m.Words = append(m.Words, word)
			}
		}
	}
	return m, nil
}

// systemdIfname reports whether systemd takes a word of Name= for an interface name or glob; it
// drops the others with a warning.
func systemdIfname(word string) bool {
	if word == "" || ifindex(word) || len(word) >= 128 || word == "." || word == ".." || word == "all" || word == "default" {
		return false
	}
	numeric := true
	for i := range len(word) {
		c := word[i]
		if c <= ' ' || c >= 127 || c == ':' || c == '/' || c == '%' {
			return false
		}
		numeric = numeric && c >= '0' && c <= '9'
	}
	return !numeric
}

// ifindex reports whether systemd reads a word as an interface index: an integer from 1 that
// fits an int, as strtol reads it in base 0, after a prefix 0b or 0o that sets base 2 or 8.
func ifindex(word string) bool {
	base := uint64(0)
	if len(word) > 2 && word[0] == '0' {
		switch word[1] {
		case 'b', 'B':
			base, word = 2, word[2:]
		case 'o', 'O':
			base, word = 8, word[2:]
		}
	}
	negative := false
	if word != "" && (word[0] == '+' || word[0] == '-') {
		negative, word = word[0] == '-', word[1:]
	}
	digit := func(c byte) uint64 {
		switch {
		case c >= '0' && c <= '9':
			return uint64(c - '0')
		case c >= 'a' && c <= 'z':
			return uint64(c-'a') + 10
		case c >= 'A' && c <= 'Z':
			return uint64(c-'A') + 10
		}
		return 36
	}
	prefixed := func(lower byte, b uint64) bool {
		return (base == 0 || base == b) && len(word) > 2 && word[0] == '0' && word[1]|0x20 == lower && digit(word[2]) < b
	}
	switch {
	case prefixed('x', 16):
		base, word = 16, word[2:]
	case prefixed('b', 2):
		base, word = 2, word[2:]
	case base == 0 && strings.HasPrefix(word, "0"):
		base = 8
	case base == 0:
		base = 10
	}
	if word == "" {
		return false
	}
	n := uint64(0)
	for i := range len(word) {
		d := digit(word[i])
		if d >= base {
			return false
		}
		if n = n*base + d; n > math.MaxInt32 {
			return false
		}
	}
	return !negative && n > 0
}

// goGlob is the fnmatch glob as path.Match takes it, which negates a bracket expression with ^
// where fnmatch takes !.
func goGlob(glob string) string {
	return strings.ReplaceAll(glob, "[!", "[^")
}

// bracketHolds reports whether a bracket expression of the glob holds c. The bracket
// expressions of fnmatch read [ as the start of a class such as [:digit:], which path.Match
// takes for the characters themselves.
func bracketHolds(glob string, c byte) bool {
	in := false
	for i := 0; i < len(glob); i++ {
		switch {
		case !in && glob[i] == '[':
			in = true
			if i+1 < len(glob) && (glob[i+1] == '!' || glob[i+1] == '^') {
				i++
			}
			if i+1 < len(glob) && glob[i+1] == ']' {
				i++
			}
		case in && glob[i] == ']':
			in = false
		case in && glob[i] == c:
			return true
		}
	}
	return false
}

// Matches reports whether networkd matches the link by this Name=.
func (m NameMatch) Matches(link string) bool {
	positive, matched := false, false
	for _, w := range m.Words {
		ok, _ := path.Match(w.Glob, link)
		if w.Negated {
			if ok {
				return false
			}
			continue
		}
		positive = true
		matched = matched || ok
	}
	return !positive || matched
}

// Positives are the words that are not negated, or a word * when there are none, as the Name=
// then matches every link the negated words leave.
func (m NameMatch) Positives() []NameWord {
	var p []NameWord
	for _, w := range m.Words {
		if !w.Negated {
			p = append(p, w)
		}
	}
	if len(p) == 0 {
		return []NameWord{{Glob: "*"}}
	}
	return p
}

// MayMatch reports whether the Name= may match a link the glob matches. It reads the negated
// words of the Name= only against a literal glob, so it may say so of a glob whose links the
// negated words exclude.
func (m NameMatch) MayMatch(glob string) bool {
	if isLiteral(glob) {
		return m.Matches(glob)
	}
	return slices.ContainsFunc(m.Positives(), func(w NameWord) bool { return GlobsOverlap(w.Glob, glob) })
}

// Covers reports whether the Name= matches every link the glob matches, as globContains finds.
func (m NameMatch) Covers(glob string) bool {
	if isLiteral(glob) {
		return m.Matches(glob)
	}
	for _, w := range m.Words {
		if w.Negated && GlobsOverlap(w.Glob, glob) {
			return false
		}
	}
	return slices.ContainsFunc(m.Positives(), func(w NameWord) bool { return globContains(w.Glob, glob) })
}

// globPrefix is the part of a glob, as path.Match takes it, before its first special character.
func globPrefix(glob string) string {
	if i := strings.IndexAny(glob, `*?[\`); i >= 0 {
		return glob[:i]
	}
	return glob
}

// isLiteral reports whether a glob matches only itself.
func isLiteral(glob string) bool {
	return globPrefix(glob) == glob
}

// GlobsOverlap reports whether two globs may match one name: two literal ones when they are
// equal, a literal and a glob when the glob matches it, and two globs unless their literal
// beginnings differ.
func GlobsOverlap(a, b string) bool {
	switch {
	case isLiteral(a):
		ok, _ := path.Match(b, a)
		return ok
	case isLiteral(b):
		ok, _ := path.Match(a, b)
		return ok
	}
	pa, pb := globPrefix(a), globPrefix(b)
	return strings.HasPrefix(pa, pb) || strings.HasPrefix(pb, pa)
}

// globContains reports whether outer matches every name inner matches: a literal inner that
// outer matches, an inner that is outer, or an inner that starts with what an outer of a literal
// beginning and * starts with. Other globs it takes for not contained.
func globContains(outer, inner string) bool {
	if isLiteral(inner) {
		ok, _ := path.Match(outer, inner)
		return ok
	}
	prefix := globPrefix(outer)
	return outer == inner || outer == prefix+"*" && strings.HasPrefix(globPrefix(inner), prefix)
}
