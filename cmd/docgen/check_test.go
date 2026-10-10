package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
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
		`testdata/docs/guides/bad.md:11: chalkctl recovery-ky w1: chalkctl has no command "recovery-ky"`,
		`testdata/docs/guides/bad.md:12: chalkctl statsu cp1: chalkctl has no command "statsu"`,
		`testdata/docs/guides/bad.md:13: chalkctl rebot w1: chalkctl has no command "rebot"`,
		`testdata/docs/guides/bad.md:14: chalkctl disks w1 --bogus: chalkctl disks has no flag --bogus`,
		`testdata/docs/guides/bad.md:15: chalklab strat: chalklab has no command "strat"`,
		`testdata/docs/guides/bad.md:16: chalkctl logs cp1 --unti chalkd.service: chalkctl logs has no flag --unti`,
		`testdata/docs/guides/bad.md:17: chalkctl logs cp1 -fx: chalkctl logs has no flag -x`,
		`testdata/docs/guides/bad.md:21: chalkctl status cp1 --bogus: chalkctl status has no flag --bogus`,
		`testdata/docs/guides/bad.md:22: chalkctl install cp1 --fingerprnt 00ff: chalkctl install has no flag --fingerprnt`,
		`testdata/docs/guides/bad.md:27: chalkctl bogus: chalkctl has no command "bogus"`,
		`testdata/docs/guides/bad.md:31: chalkctl statuss cp1: chalkctl has no command "statuss"`,
		`testdata/docs/guides/bad.md:32: chalkctl instal cp1: chalkctl has no command "instal"`,
		`testdata/docs/guides/bad.md:33: chalklab creat: chalklab has no command "creat"`,
		`testdata/docs/guides/bad.md:34: chalkctl rebot w1: chalkctl has no command "rebot"`,
		`testdata/docs/guides/bad.md:35: chalkctl logs cp1 --bogus: chalkctl logs has no flag --bogus`,
		`testdata/docs/guides/bad.md:36: chalkctl statsu cp1: chalkctl has no command "statsu"`,
		`testdata/docs/guides/bad.md:37: chalklab statsu: chalklab has no command "statsu"`,
		`testdata/docs/guides/bad.md:38: chalkctl disk w1: chalkctl has no command "disk"`,
		`testdata/docs/guides/bad.md:39: chalkctl staus cp1: chalkctl has no command "staus"`,
		`testdata/docs/guides/bad.md:40: chalkctl recovery-ky w2: chalkctl has no command "recovery-ky"`,
		`testdata/docs/guides/bad.md:44: chalkctl install cp1 --fingerprnt 00ff: chalkctl install has no flag --fingerprnt`,
	}
	got := strings.Split(err.Error(), "\n")[1:]
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("problems:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The good page's commands are all found, so the check passing them means they were checked.
func TestInvocationsOfTheGoodPage(t *testing.T) {
	blocks, err := shellBlocks("testdata/docs/index.md")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range blocks {
		for _, inv := range invocations(b) {
			got = append(got, strings.Join(inv.args, " "))
		}
	}
	want := []string{
		"chalkctl install cp1 --fingerprint 00ff --image ./image",
		"chalkctl gen secrets --plaintext",
		"chalkctl status cp1 --config=client.json",
		"chalklab create --nodes cp1,w1 --memory 1024",
		"chalkctl logs cp1 -f --unit chalkd.service",
		"chalklab console cp1 -f=false",
		"chalkctl etcd members --help",
		"chalkctl upgrade --max-unavailable N",
		"chalkctl recovery-key w1",
		"chalkctl recovery-key w2 --flake $(pwd)",
		"chalkctl status cp1 --config client.json",
		"chalklab create --nodes cp1",
		"chalkctl status cp2",
		"chalkctl status cp3",
		"chalkctl status cp4",
		"chalkctl status cp5",
		"chalkctl status cp6",
		"chalkctl status cp7 --flake $(…)",
		"chalkctl logs cp1 --unit chalkd.service",
		"chalkctl logs cp1 -f",
		"chalkctl logs cp1 -fh",
		"chalklab status",
		"chalkctl install cp1 --fingerprint 00ff",
		"chalkctl completion bash",
		"chalkctl reboot w1",
		"chalkctl status cp8",
		"chalkctl status cp9",
		"chalklab status",
		"chalkctl status cp10",
		"chalkctl status cp11",
		"chalkctl status cp12",
		"chalkctl status cp13",
		"chalkctl status cp14",
		"chalkctl status cp15",
		"chalkctl recovery-key w3",
		"chalkctl status cp16",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("invocations:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if err := check(programs, "testdata/docs/index.md"); err != nil {
		t.Errorf("the good page fails the check: %v", err)
	}
}

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

// Short flags combine as pflag combines them: booleans in a row, a flag with a value last, its
// value the rest of the word or the next word.
func TestCombinedShortFlags(t *testing.T) {
	newRoot := func() *cobra.Command {
		root := &cobra.Command{Use: "chalkctl"}
		cmd := &cobra.Command{Use: "logs", Run: func(*cobra.Command, []string) {}}
		cmd.Flags().BoolP("follow", "f", false, "")
		cmd.Flags().StringP("unit", "u", "", "")
		root.AddCommand(cmd)
		return root
	}
	for args, want := range map[string]string{
		"chalkctl logs -fh":           "",
		"chalkctl logs -fuchalkd":     "",
		"chalkctl logs -fu chalkd":    "",
		"chalkctl logs -f=false -u x": "",
		"chalkctl logs -fu":           "-u of chalkctl logs needs a value",
		"chalkctl logs -fx":           "chalkctl logs has no flag -x",
		"chalkctl logs -unit x":       "chalkctl logs has no flag -unit; long flags start with --",
	} {
		err := checkInvocation([]*cobra.Command{newRoot()}, strings.Fields(args))
		if got := ""; err != nil {
			got = err.Error()
			if got != want {
				t.Errorf("%s: %q, want %q", args, got, want)
			}
		} else if want != "" {
			t.Errorf("%s passes, want %q", args, want)
		}
	}
}
