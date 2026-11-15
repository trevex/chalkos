package chalkbuild

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/image/imagetest"
	"github.com/trevex/chalkos/pkg/internal/cli/clitest"
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
	clitest.CheckDescribed(t, NewCommand())
}

// TestSign signs a copy of an image directory and names each disk image it signed.
func TestSign(t *testing.T) {
	if _, err := exec.LookPath("sbsign"); err != nil {
		t.Skip("sbsign not in PATH")
	}
	dir := t.TempDir()
	key, cert, _ := imagetest.Signer(t, dir, "db")
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
	key, cert, _ := imagetest.Signer(t, dir, "db")
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

// TestSignNeedsCert refuses a sign without --cert as a usage error.
func TestSignNeedsCert(t *testing.T) {
	if status, _, stderr := run("sign", "--key", "db.key", "in", "out"); status != 2 || !strings.Contains(stderr, `required flag(s) "cert" not set`) {
		t.Errorf("sign without --cert = %d, %q", status, stderr)
	}
}

// TestOCISignAndPush writes an image directory's layout, signs it into a new layout and pushes
// that, printing the pushed manifest by digest.
func TestOCISignAndPush(t *testing.T) {
	if _, err := exec.LookPath("sbsign"); err != nil {
		t.Skip("sbsign not in PATH")
	}
	dir := t.TempDir()
	key, cert, _ := imagetest.Signer(t, dir, "db")
	in := filepath.Join(dir, "in")
	img := imagetest.Image{Variant: "node", Platform: "metal", Seed: "1"}
	imagetest.WriteDir(t, in, img)
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

