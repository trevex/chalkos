package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestWords(t *testing.T) {
	for line, want := range map[string][]string{
		`a "b c" 'd e' f\ g $(h) i|j&&k # l`: {"a", "b c", "d e", "f g", "$(", "h", ")", "i", "|", "j", "&&", "k", "#"},
		`echo "x $(a "b c") y" '$(d)'`:       {"echo", `x $(a "b c") y`, "$(d)"},
		`a 2>&1 b &> c <&3 d & e`:            {"a", "2>&1", "b", "&>", "c", "<&3", "d", "&", "e"},
	} {
		if got := words(line); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("words(%s) = %q, want %q", line, got, want)
		}
	}
}

// A line's simple commands end at operators and at the comment, leave out redirections and
// their targets but keep placeholders, and include the commands of substitutions, bare or
// within double quotes, each in place of the substitution.
func TestCommands(t *testing.T) {
	for line, want := range map[string][][]string{
		`a b | c d && e # f`:                   {{"a", "b"}, {"c", "d"}, {"e"}},
		`a > out b 2>&1`:                       {{"a", "b"}},
		`a <node> b`:                           {{"a", "<node>", "b"}},
		`a $(b c) d`:                           {{"b", "c"}, {"a", substitution, "d"}},
		"a `b c` d":                            {{"b", "c"}, {"a", substitution, "d"}},
		`echo "x $(chalkctl status) y" '$(z)'`: {{"echo", "x $(chalkctl status) y", "$(z)"}, {"chalkctl", "status"}},
	} {
		if got := commands(line); fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
			t.Errorf("commands(%s) = %q, want %q", line, got, want)
		}
	}
}
