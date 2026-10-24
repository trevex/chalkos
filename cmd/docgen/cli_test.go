package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// testTree is a program with a command, a group, a hidden command and a completion command.
func testTree() *cobra.Command {
	root := &cobra.Command{Use: "tool", Short: "A tool", Long: "Runs things in <dir>.", RunE: func(*cobra.Command, []string) error { return nil }}
	root.PersistentFlags().Bool("verbose", false, "print more")
	run := &cobra.Command{Use: "run <thing>", Short: "Run a thing", Long: "Runs <thing>, writing <file>.prev.", Example: "  # Run x fast.\n  tool run x --fast", RunE: func(*cobra.Command, []string) error { return nil }}
	run.Flags().Bool("fast", false, "run fast")
	run.Flags().StringP("out", "o", "", "`file` to write, a|b (default the thing's directory)")
	run.Flags().StringSlice("tag", nil, "tags of the thing")
	run.Flags().Duration("timeout", 0, "how long to wait")
	run.Flags().Int("tries", 3, "how often to try")
	run.Flags().String("key", "", "key to sign with")
	run.MarkFlagRequired("key")
	group := &cobra.Command{Use: "group", Short: "Group things"}
	group.AddCommand(&cobra.Command{Use: "add", Short: "Add a thing", RunE: func(*cobra.Command, []string) error { return nil }})
	hidden := &cobra.Command{Use: "secret", Short: "Hidden", Hidden: true, RunE: func(*cobra.Command, []string) error { return nil }}
	root.AddCommand(run, group, hidden)
	root.InitDefaultCompletionCmd()
	completion, _, _ := root.Find([]string{"completion"})
	completion.Short = "Write a shell completion script"
	return root
}

func TestWriteCLI(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "zensical.toml")
	os.WriteFile(config, []byte("nav = [\n  { \"Reference\" = [\n    # BEGIN docgen cli\n    \"stale.md\",\n    # END docgen cli\n  ] },\n]\n"), 0o644)
	out := filepath.Join(dir, "cli")
	os.MkdirAll(out, 0o755)
	os.WriteFile(filepath.Join(out, "tool_gone.md"), nil, 0o644)
	if err := writeCLI([]*cobra.Command{testTree()}, out, config, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	var names []string
	entries, _ := os.ReadDir(out)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if got, want := strings.Join(names, " "), "index.md tool.md tool_completion.md tool_group.md tool_group_add.md tool_run.md"; got != want {
		t.Errorf("pages = %s, want %s", got, want)
	}
	index, _ := os.ReadFile(filepath.Join(out, "index.md"))
	if !strings.Contains(string(index), "Every command shows its description, examples and flags with `--help`") {
		t.Errorf("index.md does not say what --help shows:\n%s", index)
	}
	page, _ := os.ReadFile(filepath.Join(out, "tool_run.md"))
	wantPage := "---\ntitle: \"tool run\"\ndescription: \"Run a thing\"\n---\n\n" +
		"# tool run\n\n" +
		"```sh\ntool run <thing> [flags]\n```\n\n" +
		"Runs &lt;thing>, writing &lt;file>.prev.\n\n" +
		"## Flags\n\n" +
		"| Flag | Type | Default | Description |\n" +
		"| --- | --- | --- | --- |\n" +
		"| `--fast`{ #flag-fast } | boolean | `false` | Run fast. |\n" +
		"| `--key`{ #flag-key } | string | required | Key to sign with. |\n" +
		"| `--out`{ #flag-out }, `-o` | string | the thing's directory | File to write, a\\|b. |\n" +
		"| `--tag`{ #flag-tag } | list of string | — | Tags of the thing. |\n" +
		"| `--timeout`{ #flag-timeout } | duration | `0s` | How long to wait. |\n" +
		"| `--tries`{ #flag-tries } | integer | `3` | How often to try. |\n\n" +
		"## Inherited flags\n\n" +
		"| Flag | Type | Default | Description |\n" +
		"| --- | --- | --- | --- |\n" +
		"| `--verbose`{ #flag-verbose } | boolean | `false` | Print more. |\n\n" +
		"## Examples\n\n" +
		"```sh\n# Run x fast.\ntool run x --fast\n```\n\n" +
		"## See also\n\n" +
		"- [tool](tool.md): A tool\n"
	if string(page) != wantPage {
		t.Errorf("tool_run.md =\n%s\nwant\n%s", page, wantPage)
	}
	page, _ = os.ReadFile(filepath.Join(out, "tool.md"))
	for _, want := range []string{
		"# tool\n\nRuns things in &lt;dir>.\n\n## Flags\n",
		"## See also\n\n- [tool completion](tool_completion.md): Write a shell completion script\n- [tool group](tool_group.md): Group things\n- [tool run](tool_run.md): Run a thing\n",
	} {
		if !strings.Contains(string(page), want) {
			t.Errorf("tool.md lacks %q:\n%s", want, page)
		}
	}
	nav, _ := os.ReadFile(config)
	want := `nav = [
  { "Reference" = [
    # BEGIN docgen cli
    { "Command line" = [
      "reference/cli/index.md",
      { "tool" = [
        { "Overview" = "reference/cli/tool.md" },
        { "completion" = "reference/cli/tool_completion.md" },
        { "group" = [
          { "Overview" = "reference/cli/tool_group.md" },
          { "add" = "reference/cli/tool_group_add.md" },
        ] },
        { "run" = "reference/cli/tool_run.md" },
      ] },
    ] },
    # END docgen cli
  ] },
]
`
	if string(nav) != want {
		t.Errorf("zensical.toml =\n%s\nwant\n%s", nav, want)
	}
}

// The completion command gets one page, with every shell's usage line and the command that loads
// its script, and none for each shell.
func TestCompletionPage(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "zensical.toml")
	os.WriteFile(config, []byte("# BEGIN docgen cli\n# END docgen cli\n"), 0o644)
	if err := writeCLI([]*cobra.Command{testTree()}, filepath.Join(dir, "cli"), config, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	page, _ := os.ReadFile(filepath.Join(dir, "cli", "tool_completion.md"))
	for _, want := range []string{
		"description: \"Write a shell completion script\"",
		"```sh\ntool completion bash [flags]\ntool completion fish [flags]\ntool completion powershell [flags]\ntool completion zsh [flags]\n```\n",
		"| `--no-descriptions`{ #flag-no-descriptions } | boolean | `false` |",
		"## Shells\n\n| Shell | Current session | Every new session |\n| --- | --- | --- |\n" +
			"| bash | `source <(tool completion bash)` | With the bash-completion package installed, `tool completion bash > ~/.local/share/bash-completion/completions/tool` once. |\n",
		"| fish | `tool completion fish | source` | `tool completion fish > ~/.config/fish/completions/tool.fish` once. |\n",
		"| powershell | `tool completion powershell | Out-String | Invoke-Expression` | `tool completion powershell | Out-String | Invoke-Expression` in the PowerShell profile, `$PROFILE`. |\n",
		"| zsh | `source <(tool completion zsh)` | With completion enabled by `autoload -U compinit; compinit` in `~/.zshrc`, `tool completion zsh > \"${fpath[1]}/_tool\"` once. |\n",
		"## See also\n\n- [tool](tool.md): A tool\n",
	} {
		if !strings.Contains(string(page), want) {
			t.Errorf("tool_completion.md lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(string(page), "tool_completion_bash.md") {
		t.Errorf("tool_completion.md links a page per shell:\n%s", page)
	}
}

func TestWriteCLINeedsMarkers(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "zensical.toml")
	os.WriteFile(config, []byte("nav = []\n"), 0o644)
	if err := writeCLI([]*cobra.Command{testTree()}, filepath.Join(dir, "cli"), config, t.TempDir()); err == nil || !strings.Contains(err.Error(), "BEGIN docgen cli") {
		t.Errorf("err = %v, want one naming the markers", err)
	}
}

func TestProseKeepsCode(t *testing.T) {
	in := "a <b>\n\n```\nx <y>\n```\n\n\tsource <(tool completion bash)\n    <z>"
	want := "a &lt;b>\n\n```\nx <y>\n```\n\n\tsource <(tool completion bash)\n    <z>"
	if got := prose(in); got != want {
		t.Errorf("prose =\n%q\nwant\n%q", got, want)
	}
}

// The programs' pages hold every command a user can run; chalkcfg, which runs on nodes without
// logins, has no completion command.
func TestProgramsHaveCompletion(t *testing.T) {
	var names []string
	for _, root := range programs() {
		names = append(names, root.Name())
		cmd, _, err := root.Find([]string{"completion", "bash"})
		has := err == nil && cmd.Name() == "bash"
		if has == root.CompletionOptions.DisableDefaultCmd {
			t.Errorf("%s: completion command %v, disabled %v", root.Name(), has, root.CompletionOptions.DisableDefaultCmd)
		}
	}
	if got := strings.Join(names, " "); got != "chalkctl chalklab chalkbuild chalkcfg" {
		t.Errorf("programs = %s", got)
	}
}

// A command with subcommands runs only to report a usage error, so its page shows no usage line.
func TestGroupPageHasNoUsageLine(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "zensical.toml")
	os.WriteFile(config, []byte("# BEGIN docgen cli\n# END docgen cli\n"), 0o644)
	if err := writeCLI([]*cobra.Command{testTree()}, filepath.Join(dir, "cli"), config, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	page, _ := os.ReadFile(filepath.Join(dir, "cli", "tool.md"))
	if strings.Contains(string(page), "[flags]") {
		t.Errorf("tool.md shows a usage line:\n%s", page)
	}
}

// The completion command's page describes it in the site's words instead of cobra's.
func TestCompletionDescribed(t *testing.T) {
	for _, root := range programs() {
		if root.CompletionOptions.DisableDefaultCmd {
			continue
		}
		cmd, _, _ := root.Find([]string{"completion"})
		if strings.Contains(cmd.Short+cmd.Long, "autocompletion") || !strings.Contains(cmd.Long, root.Name()+"'s commands") || !strings.Contains(cmd.Example, root.Name()+" completion bash") {
			t.Errorf("%s completion has cobra's description or no example:\n%s\n%s", root.Name(), cmd.Long, cmd.Example)
		}
	}
}

// chalkcfg's root keeps its own description.
func TestChalkcfgRootDescribed(t *testing.T) {
	for _, root := range programs() {
		if root.Name() == "chalkcfg" && strings.Contains(root.Long, "completes") {
			t.Errorf("chalkcfg's description is the completion command's:\n%s", root.Long)
		}
	}
}

// A link of help to the site becomes a link to the page, titled by the heading it points to or
// else by the page's title.
func TestSiteLinks(t *testing.T) {
	docs := t.TempDir()
	os.MkdirAll(filepath.Join(docs, "how-it-works", "node"), 0o755)
	os.WriteFile(filepath.Join(docs, "how-it-works", "node", "upgrades.md"), []byte("---\ntitle: \"Upgrades\"\n---\n\n# Upgrades\n\n## A rolled-back node stops the run\n\n## The `chalkd` [slot](x.md) writes { #slot-writes }\n"), 0o644)
	in := "See https://trevex.github.io/chalkos/how-it-works/node/upgrades/ and\nhttps://trevex.github.io/chalkos/how-it-works/node/upgrades/#a-rolled-back-node-stops-the-run,\nhttps://trevex.github.io/chalkos/how-it-works/node/upgrades/#slot-writes."
	want := "See [Upgrades](../../how-it-works/node/upgrades.md) and\n[A rolled-back node stops the run](../../how-it-works/node/upgrades.md#a-rolled-back-node-stops-the-run),\n[The chalkd slot writes](../../how-it-works/node/upgrades.md#slot-writes)."
	got, err := siteLinks(in, docs)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("siteLinks =\n%s\nwant\n%s", got, want)
	}
	for _, bad := range []string{
		"https://trevex.github.io/chalkos/how-it-works/node/gone/",
		"https://trevex.github.io/chalkos/how-it-works/node/upgrades/#gone",
	} {
		if _, err := siteLinks(bad, docs); err == nil {
			t.Errorf("siteLinks(%s) found a page", bad)
		}
	}
}

// addresses is a flag value of a type pflag does not know, as chalkctl rotate's --address.
type addresses map[string]string

func (addresses) String() string   { return "" }
func (addresses) Set(string) error { return nil }
func (addresses) Type() string     { return "nodeAddresses" }

// unknownValue is a flag value of a type docgen does not name.
type unknownValue struct{}

func (unknownValue) String() string   { return "" }
func (unknownValue) Set(string) error { return nil }
func (unknownValue) Type() string     { return "ipNet" }

// A flag's type is in the words of the page of config kinds; one docgen does not know shows as
// pflag names it.
func TestFlagType(t *testing.T) {
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	fs.Bool("bool", false, "")
	fs.StringToString("map", nil, "")
	fs.StringSlice("list", nil, "")
	fs.Var(addresses{}, "address", "")
	fs.Var(unknownValue{}, "net", "")
	for name, want := range map[string]string{
		"bool":    "boolean",
		"map":     "map of string",
		"list":    "list of string",
		"address": "map of string",
		"net":     "ipNet",
	} {
		if got := flagType(fs.Lookup(name)); got != want {
			t.Errorf("flagType(--%s) = %q, want %q", name, got, want)
		}
	}
}

// A default the help names at its end is cut off and kept; parentheses elsewhere stay.
func TestDescribedDefault(t *testing.T) {
	for usage, want := range map[string][]string{
		"file to write (default the thing's directory)":                       {" (default the thing's directory)", "the thing's directory"},
		"client file (default $CHALKOSCONFIG, else ~/.config/chalkos/config)": {" (default $CHALKOSCONFIG, else ~/.config/chalkos/config)", "$CHALKOSCONFIG, else ~/.config/chalkos/config"},
		"how long to wait (default 5m) for the node":                          nil,
		"a size (in bytes)":                 nil,
		"nested (default the (first) node)": nil,
	} {
		if got := describedDefault.FindStringSubmatch(usage); !slices.Equal(got, want) {
			t.Errorf("describedDefault in %q = %q, want %q", usage, got, want)
		}
	}
}
