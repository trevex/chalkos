package chalkctl

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// execute runs args as the chalkctl program does and returns its exit status.
func (a *app) execute(ctx context.Context, args ...string) int {
	root := newCommand(a)
	root.SetArgs(args)
	root.SetIn(a.stdin)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	return Execute(ctx, root)
}

func TestExecuteUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		nil, {"bogus"}, {"etcd"}, {"etcd", "bogus"}, {"gen"},
		// Leaves: wrong positional arguments, unknown flags, required flags not set and flags
		// that do not go together.
		{"status"}, {"status", "a", "b"}, {"etcd", "members", "x"}, {"node", "revoke"},
		{"node", "diag"}, {"node", "diag", "n1", "ping", "address"}, {"node", "diag", "n1", "ping", "=1"},
		{"node", "diag", "n1", "ping", "count=1", "count=2"},
		{"gen", "secrets", "--plaintext", "extra"}, {"gen", "secrets"},
		{"status", "--bogus"}, {"status", "n1", "--bogus"},
		{"config", "new"}, {"config", "new", "--name", "ada"},
		{"upgrade", "--max-unavailable", "0"}, {"rotate", "os-ca", "--resume", "--finish"},
	} {
		ta := newTestApp(t)
		if got := ta.execute(context.Background(), args...); got != 2 {
			t.Errorf("chalkctl %v exits with %d, want 2", args, got)
		}
		if !strings.Contains(ta.stderr.String(), "Usage:") {
			t.Errorf("chalkctl %v prints no usage:\n%s", args, ta.stderr.String())
		}
		if len(args) > 0 && args[len(args)-1] == "bogus" && !strings.Contains(ta.stderr.String(), `unknown command "bogus"`) {
			t.Errorf("chalkctl %v does not name the unknown command:\n%s", args, ta.stderr.String())
		}
	}
}

// A flag error on the root or on a command with subcommands is a usage error, as an unknown
// command is. A flag before a subcommand's name is one, in either form.
func TestExecuteFlagUsageErrors(t *testing.T) {
	for _, c := range []struct {
		args []string
		flag string
	}{
		{[]string{"--bogus"}, "--bogus"},
		{[]string{"etcd", "--bogus"}, "--bogus"},
		{[]string{"etcd", "--force", "leave", "n1"}, "--force"},
		{[]string{"etcd", "--force=true", "leave", "n1"}, "--force"},
		{[]string{"etcd", "--via=cp2", "members"}, "--via"},
		{[]string{"etcd", "--via", "cp2", "members"}, "--via"},
		{[]string{"--insecure", "install", "cp1"}, "--insecure"},
		{[]string{"--insecure=true", "install", "cp1"}, "--insecure"},
	} {
		args, flag := c.args, c.flag
		ta := newTestApp(t)
		if got := ta.execute(context.Background(), args...); got != 2 {
			t.Errorf("chalkctl %v exits with %d, want 2", args, got)
		}
		if stderr := ta.stderr.String(); !strings.Contains(stderr, "unknown flag: "+flag) || !strings.Contains(stderr, "Usage:") {
			t.Errorf("chalkctl %v does not name %s and print the usage:\n%s", args, flag, stderr)
		}
	}
}

func TestExecuteErrors(t *testing.T) {
	ta := newTestApp(t)
	if got := ta.execute(context.Background(), "status", "n1", "--inventory", filepath.Join(ta.dir, "missing.yaml")); got != 1 {
		t.Errorf("a failing command exits with %d, want 1", got)
	}
	if !strings.HasPrefix(ta.stderr.String(), "chalkctl: ") {
		t.Errorf("stderr = %q, want the error after chalkctl:", ta.stderr.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ta = newTestApp(t)
	if got := ta.execute(ctx, "status", "n1", "--inventory", filepath.Join(ta.dir, "missing.yaml")); got != 130 {
		t.Errorf("a command whose context ended exits with %d, want 130", got)
	}
}

// Shell completion comes from cobra's completion command.
func TestCompletion(t *testing.T) {
	ta := newTestApp(t)
	if err := ta.run(context.Background(), []string{"completion", "bash"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ta.stdout.String(), "__start_chalkctl") {
		t.Errorf("chalkctl completion bash prints no completion script:\n%.200s", ta.stdout.String())
	}
}

func TestExecuteFlags(t *testing.T) {
	ta := newTestApp(t)
	if got := ta.execute(context.Background(), "install", "n1", "--bogus"); got != 2 {
		t.Errorf("an unknown flag exits with %d, want 2", got)
	}
	if want := "chalkctl: usage: unknown flag: --bogus\nUsage:\n  chalkctl install <node> [flags]\n"; !strings.HasPrefix(ta.stderr.String(), want) {
		t.Errorf("stderr = %q, want it to start with %q", ta.stderr.String(), want)
	}
	// Single-dash long flags are shorthands to pflag.
	ta = newTestApp(t)
	if got := ta.execute(context.Background(), "install", "n1", "-insecure"); got != 2 {
		t.Errorf("-insecure exits with %d, want 2", got)
	}
	ta = newTestApp(t)
	if got := ta.execute(context.Background(), "logs", "--help"); got != 0 {
		t.Errorf("--help exits with %d, want 0", got)
	}
	if !strings.Contains(ta.stdout.String(), "-f, --follow") {
		t.Errorf("logs --help lacks -f, --follow:\n%s", ta.stdout.String())
	}
}

// The help names the value of --address NODE=ADDR.
func TestAddressValueName(t *testing.T) {
	for _, name := range []string{"rotate", "upgrade"} {
		ta := newTestApp(t)
		if got := ta.execute(context.Background(), name, "--help"); got != 0 {
			t.Fatalf("%s --help exits with %d", name, got)
		}
		if !strings.Contains(ta.stdout.String(), "--address NODE=ADDR ") {
			t.Errorf("%s --help does not show --address NODE=ADDR:\n%s", name, ta.stdout.String())
		}
	}
}

// Every command is described for its help and the reference pages.
func TestCommandsDescribed(t *testing.T) {
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Short == "" || cmd.Long == "" || cmd.Example == "" {
			t.Errorf("%s lacks a short or long description or examples", cmd.CommandPath())
		}
		for _, c := range cmd.Commands() {
			walk(c)
		}
	}
	walk(newCommand(&app{}))
}
