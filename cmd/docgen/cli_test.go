package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// testTree is a program with a command, a group and a hidden command.
func testTree() *cobra.Command {
	root := &cobra.Command{Use: "tool", Short: "A tool", Long: "Runs things in <dir>.", RunE: func(*cobra.Command, []string) error { return nil }}
	run := &cobra.Command{Use: "run <thing>", Short: "Run a thing", Long: "Runs <thing>, writing <file>.prev.", Example: "  tool run x --fast", RunE: func(*cobra.Command, []string) error { return nil }}
	run.Flags().Bool("fast", false, "run fast")
	group := &cobra.Command{Use: "group", Short: "Group things"}
	group.AddCommand(&cobra.Command{Use: "add", Short: "Add a thing", RunE: func(*cobra.Command, []string) error { return nil }})
	hidden := &cobra.Command{Use: "secret", Short: "Hidden", Hidden: true, RunE: func(*cobra.Command, []string) error { return nil }}
	root.AddCommand(run, group, hidden)
	return root
}

func TestWriteCLI(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "zensical.toml")
	os.WriteFile(config, []byte("nav = [\n  { \"Reference\" = [\n    # BEGIN docgen cli\n    \"stale.md\",\n    # END docgen cli\n  ] },\n]\n"), 0o644)
	out := filepath.Join(dir, "cli")
	os.MkdirAll(out, 0o755)
	os.WriteFile(filepath.Join(out, "tool_gone.md"), nil, 0o644)
	if err := writeCLI([]*cobra.Command{testTree()}, out, config); err != nil {
		t.Fatal(err)
	}
	var names []string
	entries, _ := os.ReadDir(out)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if got, want := strings.Join(names, " "), "index.md tool.md tool_group.md tool_group_add.md tool_run.md"; got != want {
		t.Errorf("pages = %s, want %s", got, want)
	}
	page, _ := os.ReadFile(filepath.Join(out, "tool_run.md"))
	for _, want := range []string{
		"---\ntitle: \"tool run\"\ndescription: \"Run a thing\"\n---\n\n# tool run\n",
		"## Synopsis\n\nRuns &lt;thing>, writing &lt;file>.prev.",
		"```\ntool run <thing> [flags]\n```",
		"## Examples\n\n```\n  tool run x --fast\n```",
		"* [tool](tool.md)",
	} {
		if !strings.Contains(string(page), want) {
			t.Errorf("tool_run.md lacks %q:\n%s", want, page)
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

func TestWriteCLINeedsMarkers(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "zensical.toml")
	os.WriteFile(config, []byte("nav = []\n"), 0o644)
	if err := writeCLI([]*cobra.Command{testTree()}, filepath.Join(dir, "cli"), config); err == nil || !strings.Contains(err.Error(), "BEGIN docgen cli") {
		t.Errorf("err = %v, want one naming the markers", err)
	}
}

func TestMarkdownKeepsCode(t *testing.T) {
	in := "## a <b>\n\n### c\n\n```\n## x <y>\n```\n\n\tsource <(tool completion bash)\n"
	want := "# a &lt;b>\n\n## c\n\n```\n## x <y>\n```\n\n\tsource <(tool completion bash)\n"
	if got := markdown(in); got != want {
		t.Errorf("markdown =\n%q\nwant\n%q", got, want)
	}
}

// The programs' pages hold every command a user can run.
func TestProgramsHaveCompletion(t *testing.T) {
	for _, root := range programs() {
		if cmd, _, err := root.Find([]string{"completion", "bash"}); err != nil || cmd.Name() != "bash" {
			t.Errorf("%s has no completion command: %v", root.Name(), err)
		}
	}
}

// A command with subcommands runs only to report a usage error, so its page shows no usage line.
func TestGroupPageHasNoUsageLine(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "zensical.toml")
	os.WriteFile(config, []byte("# BEGIN docgen cli\n# END docgen cli\n"), 0o644)
	if err := writeCLI([]*cobra.Command{testTree()}, filepath.Join(dir, "cli"), config); err != nil {
		t.Fatal(err)
	}
	page, _ := os.ReadFile(filepath.Join(dir, "cli", "tool.md"))
	if strings.Contains(string(page), "[flags]") {
		t.Errorf("tool.md shows a usage line:\n%s", page)
	}
}
