package main

import (
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	err := check(programs, "testdata/docs")
	if err == nil {
		t.Fatal("check found no bad command")
	}
	want := []string{
		`testdata/docs/guides/bad.md:4: chalkctl instal cp1: chalkctl has no command "instal"`,
		`testdata/docs/guides/bad.md:5: chalkctl etcd membres: chalkctl etcd has no command "membres"`,
		`testdata/docs/guides/bad.md:6: chalkctl install cp1 --fingerprnt 00ff: chalkctl install has no flag --fingerprnt`,
		`testdata/docs/guides/bad.md:7: chalklab create --nodes: --nodes of chalklab create needs a value`,
		`testdata/docs/guides/bad.md:8: chalkctl install cp1 -insecure: chalkctl install has no flag -insecure; long flags start with --`,
		`testdata/docs/guides/bad.md:9: chalkctl logs cp1 -x: chalkctl logs has no flag -x`,
		`testdata/docs/guides/bad.md:10: chalklab destroy --force: chalklab destroy has no flag --force`,
		`testdata/docs/guides/bad.md:14: chalkctl status cp1 --bogus: chalkctl status has no flag --bogus`,
	}
	got := strings.Split(err.Error(), "\n")[1:]
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("problems:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestWords(t *testing.T) {
	got := words(`a "b c" 'd e' f\ g $(h) i|j&&k # l`)
	want := []string{"a", "b c", "d e", "f g", "$(", "h", ")", "i", "|", "j", "&&", "k", "#"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("words = %q, want %q", got, want)
	}
}
