package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// tree is a root with a group and leaves that take one argument, a required flag, and fail.
func tree(stderr *bytes.Buffer) *cobra.Command {
	root := Root(&cobra.Command{Use: "tool", TraverseChildren: true})
	group := Group(&cobra.Command{Use: "group"})
	one := &cobra.Command{Use: "one <arg>", Args: ExactArgs(1), RunE: func(*cobra.Command, []string) error { return nil }}
	one.Flags().String("name", "", "")
	required := &cobra.Command{Use: "required", Args: NoArgs, RunE: func(*cobra.Command, []string) error { return nil }}
	required.Flags().String("name", "", "")
	required.MarkFlagRequired("name")
	fail := &cobra.Command{Use: "fail", Args: RangeArgs(0, 1), RunE: func(*cobra.Command, []string) error { return errors.New("failed") }}
	group.AddCommand(one)
	root.AddCommand(group, required, fail)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(stderr)
	return root
}

func TestExecute(t *testing.T) {
	for args, want := range map[string]int{
		"group one a": 0, "required --name x": 0, "fail": 1, "fail a": 1, "--help": 0,
		"": 2, "bogus": 2, "group": 2, "group bogus": 2, "--bogus": 2, "group --name x one a": 2,
		"group one": 2, "group one a b": 2, "group one a --bogus": 2, "required": 2, "required a --name x": 2,
		"fail a b": 2,
	} {
		var stderr bytes.Buffer
		root := tree(&stderr)
		root.SetArgs(strings.Fields(args))
		if got := Execute(context.Background(), root, Options{}); got != want {
			t.Errorf("tool %s exits with %d, want %d:\n%s", args, got, want, stderr.String())
		}
		if usage := strings.Contains(stderr.String(), "Usage:"); usage != (want == 2) {
			t.Errorf("tool %s prints the usage: %t, want %t:\n%s", args, usage, want == 2, stderr.String())
		}
	}
}

func TestExecuteMessages(t *testing.T) {
	for args, want := range map[string]string{
		"":          "Usage:\n  tool [flags]\n  tool [command]\n",
		"fail":      "tool: failed\n",
		"group one": "tool: usage: accepts 1 arg(s), received 0\nUsage:\n  tool group one <arg> [flags]\n",
		"required":  "tool: usage: required flag(s) \"name\" not set\nUsage:\n  tool required [flags]\n",
	} {
		var stderr bytes.Buffer
		root := tree(&stderr)
		root.SetArgs(strings.Fields(args))
		Execute(context.Background(), root, Options{})
		if !strings.HasPrefix(stderr.String(), want) {
			t.Errorf("tool %s prints %q, want it to start with %q", args, stderr.String(), want)
		}
	}
}

// An error after the context ended exits with 130 when the tree is interruptible.
func TestExecuteInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []struct {
		args string
		opts Options
		want int
	}{
		{"fail", Options{Interruptible: true}, 130},
		{"fail", Options{}, 1},
		{"group one a", Options{Interruptible: true}, 0},
	} {
		root := tree(&bytes.Buffer{})
		root.SetArgs(strings.Fields(c.args))
		if got := Execute(ctx, root, c.opts); got != c.want {
			t.Errorf("tool %s with %+v exits with %d, want %d", c.args, c.opts, got, c.want)
		}
	}
}

func TestUsagef(t *testing.T) {
	err := Usagef("pass %s", "--name")
	if !errors.Is(err, ErrUsage) || err.Error() != "usage: pass --name" {
		t.Errorf("Usagef = %v", err)
	}
}
