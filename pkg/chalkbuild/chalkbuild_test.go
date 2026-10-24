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
	"github.com/trevex/chalkos/pkg/ociimage"
	"github.com/trevex/chalkos/pkg/ociimage/ocitest"
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
		{[]string{"sign", "--key", "db.key", "in", "out"}, `required flag(s) "cert" not set`},
		{[]string{"sign", "--cert", "db.crt", "in", "out"}, `required flag(s) "key" not set`},
		{[]string{"sign", "--key", "db.key", "--cert", "db.crt", "in"}, "usage: chalkbuild sign"},
	} {
		if status, _, stderr := run(tc.args...); status != 1 || !strings.Contains(stderr, tc.want) {
			t.Errorf("%v = %d, %q; want %q", tc.args, status, stderr, tc.want)
		}
	}
}

// TestOCISignAndPush writes an image directory's layout, signs it into a new layout and pushes
// that, printing the pushed manifest by digest.
func TestOCISignAndPush(t *testing.T) {
	if _, err := exec.LookPath("sbsign"); err != nil {
		t.Skip("sbsign not in PATH")
	}
	dir := t.TempDir()
	key, cert := imagetest.Signer(t, dir, "db")
	in := filepath.Join(dir, "in")
	img := imagetest.Image{Variant: "node", Platform: "metal", Seed: "1"}
	imagetest.WriteContract(t, in, img, imagetest.Write(t, in, img))
	layout := filepath.Join(dir, "oci", "node-metal")
	if status, stdout, stderr := run("oci", in, layout); status != 0 || !strings.HasPrefix(stdout, "wrote "+layout+", manifest sha256:") {
		t.Fatalf("oci = %d, stdout %q, stderr %q", status, stdout, stderr)
	}
	signed := filepath.Join(dir, "signed", "node-metal")
	if status, stdout, stderr := run("sign", "--key", key, "--cert", cert, layout, signed); status != 0 || !strings.HasPrefix(stdout, "signed "+signed+", manifest sha256:") {
		t.Fatalf("sign = %d, stdout %q, stderr %q", status, stdout, stderr)
	}
	signedImg, err := ociimage.ReadLayout(signed)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := signedImg.Digest()
	host := ocitest.Registry(t)
	if status, stdout, stderr := run("push", signed, host+"/chalkos/node-metal:v1"); status != 0 || stdout != host+"/chalkos/node-metal@"+d.String()+"\n" {
		t.Errorf("push = %d, stdout %q, stderr %q", status, stdout, stderr)
	}
}

// TestOCIAndPushNeedTwoArguments refuses oci and push without both of their arguments.
func TestOCIAndPushNeedTwoArguments(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"oci", "in"}, "usage: chalkbuild oci"},
		{[]string{"push", "layout"}, "usage: chalkbuild push"},
	} {
		if status, _, stderr := run(tc.args...); status != 1 || !strings.Contains(stderr, tc.want) {
			t.Errorf("%v = %d, %q; want %q", tc.args, status, stderr, tc.want)
		}
	}
}
