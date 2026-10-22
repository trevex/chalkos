package chalkbuild

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/image/imagetest"
)

// run runs chalkbuild with the arguments and returns its exit status and output.
func run(args ...string) (status int, stdout, stderr string) {
	root := NewCommand()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	status = Execute(context.Background(), root)
	return status, out.String(), errOut.String()
}

// Every command is described for its help and the reference pages.
func TestCommandsDescribed(t *testing.T) {
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if !cmd.Hidden && (cmd.Short == "" || cmd.Long == "" || cmd.Example == "") {
			t.Errorf("%s lacks a short or long description or examples", cmd.CommandPath())
		}
		for _, c := range cmd.Commands() {
			walk(c)
		}
	}
	walk(NewCommand())
}

// TestSign signs a copy of an image directory and names each disk image it signed.
func TestSign(t *testing.T) {
	if _, err := exec.LookPath("sbsign"); err != nil {
		t.Skip("sbsign not in PATH")
	}
	dir := t.TempDir()
	key, cert := imagetest.Signer(t, dir, "db")
	in := filepath.Join(dir, "in")
	imagetest.Write(t, in, imagetest.Image{Variant: "node", Platform: "metal", Seed: "1"})
	out := filepath.Join(dir, "out")
	status, stdout, stderr := run("sign", "--key", key, "--cert", cert, in, out)
	if status != 0 || stdout != "signed "+filepath.Join(out, "chalkos.raw")+"\n" {
		t.Errorf("sign = %d, stdout %q, stderr %q", status, stdout, stderr)
	}
}

// TestSignMakesOutsParent signs into a directory whose parent does not exist yet, as an
// inventory's own layout need not either.
func TestSignMakesOutsParent(t *testing.T) {
	if _, err := exec.LookPath("sbsign"); err != nil {
		t.Skip("sbsign not in PATH")
	}
	dir := t.TempDir()
	key, cert := imagetest.Signer(t, dir, "db")
	in := filepath.Join(dir, "in")
	imagetest.Write(t, in, imagetest.Image{Variant: "node", Platform: "metal", Seed: "1"})
	out := filepath.Join(dir, "signed", "node-metal")
	status, _, stderr := run("sign", "--key", key, "--cert", cert, in, out)
	if status != 0 {
		t.Fatalf("sign = %d, stderr %q", status, stderr)
	}
	if _, err := os.Stat(filepath.Join(out, "chalkos.raw")); err != nil {
		t.Errorf("the signed copy: %v", err)
	}
}

// TestSignNeedsKeyCertAndTwoDirectories refuses a sign without --key or --cert, or without both
// directories.
func TestSignNeedsKeyCertAndTwoDirectories(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"sign", "--key", "db.key", "in", "out"}, "needs --key and --cert"},
		{[]string{"sign", "--cert", "db.crt", "in", "out"}, "needs --key and --cert"},
		{[]string{"sign", "--key", "db.key", "--cert", "db.crt", "in"}, "usage: chalkbuild sign"},
	} {
		if status, _, stderr := run(tc.args...); status != 1 || !strings.Contains(stderr, tc.want) {
			t.Errorf("%v = %d, %q; want %q", tc.args, status, stderr, tc.want)
		}
	}
}
