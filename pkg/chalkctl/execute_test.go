package chalkctl

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/internal/cli/clitest"
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
	ta := newTestApp(t)
	for _, args := range [][]string{
		nil, {"bogus"},
		// chalkctl's own argument checks and flag constraints.
		{"node", "diag", "n1", "ping", "=1"}, {"node", "diag", "n1", "ping", "count=1", "count=2"},
		{"gen", "secrets"}, {"client", "new", "--name", "ada"},
		{"upgrade", "--max-unavailable", "0"}, {"rotate", "os-ca", "--resume", "--finish"},
		{"node", "reboot", "n1", "--timeout", "-1s"}, {"config", "confirm", "n1", "--try", "1m"},
		{"config", "--address", "127.0.0.1", "confirm", "n1"},
	} {
		ta.stderr.Reset()
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

// A flag error on the root, or a flag before a subcommand's name, is a usage error.
func TestExecuteFlagUsageErrors(t *testing.T) {
	for _, c := range []struct {
		args []string
		flag string
	}{
		{[]string{"--bogus"}, "--bogus"},
		{[]string{"etcd", "--through-others", "leave", "n1"}, "--through-others"},
		{[]string{"etcd", "--via=cp2", "members"}, "--via"},
		{[]string{"--insecure", "install", "cp1"}, "--insecure"},
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
	ta.execute(context.Background(), "install", "n1", "--bogus")
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

// The help names the value of --address [NODE=]ADDR, or NODE=ADDR for rotate, which takes no bare
// ADDR, and says which node a bare ADDR reaches.
func TestAddressValueName(t *testing.T) {
	single := "address of a node's chalkd: ADDR for the one node the command reaches, or NODE=ADDR for any node, where ADDR is host or host:port; may be repeated"
	several := "address of a node's chalkd as NODE=ADDR, or as ADDR alone when the command names one node, where ADDR is host or host:port; may be repeated"
	named := "address of a node's chalkd as NODE=ADDR, where ADDR is host or host:port; may be repeated"
	for _, c := range []struct {
		args  []string
		value string
		usage string
	}{
		{[]string{"status"}, "[NODE=]ADDR", single},
		{[]string{"node", "reboot"}, "[NODE=]ADDR", single},
		{[]string{"config", "apply"}, "[NODE=]ADDR", several},
		{[]string{"rotate"}, "NODE=ADDR", named},
		{[]string{"upgrade"}, "[NODE=]ADDR", several},
	} {
		ta := newTestApp(t)
		if got := ta.execute(context.Background(), append(c.args, "--help")...); got != 0 {
			t.Fatalf("%v --help exits with %d", c.args, got)
		}
		out := ta.stdout.String()
		if !strings.Contains(out, "--address "+c.value+" ") {
			t.Errorf("%v --help does not show --address %s:\n%s", c.args, c.value, out)
		}
		if !strings.Contains(out, c.usage) {
			t.Errorf("%v --help does not describe --address as %q:\n%s", c.args, c.usage, out)
		}
	}
}

// Every command is described for its help and the reference pages.
func TestCommandsDescribed(t *testing.T) {
	clitest.CheckDescribed(t, newCommand(&app{}))
}
