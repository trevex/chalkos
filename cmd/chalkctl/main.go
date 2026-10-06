// Command chalkctl builds, signs, installs, and operates chalkos clusters.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"

	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/imagesign"
)

const usage = `usage: chalkctl <command> [flags]

commands:
  gen secrets (--recipient R... | --plaintext)  generate the cluster's secrets file
  recovery-key <node>                           print a node's recovery key
  sign                                          sign the boot loader and UKIs of a disk image

Run chalkctl <command> -h for the flags of a command.`

// app is chalkctl with its environment, so tests can run commands in process.
type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	// nix runs nix and returns its standard output.
	nix func(ctx context.Context, args ...string) ([]byte, error)
	// home is the user's home directory, where age and SSH keys are looked up.
	home string
}

func main() {
	home, _ := os.UserHomeDir()
	a := &app{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, nix: runNix, home: home}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := a.run(ctx, os.Args[1:])
	if errors.Is(err, errUsage) {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "chalkctl:", err)
		os.Exit(1)
	}
}

var errUsage = errors.New("usage")

func (a *app) run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	cmd, rest := args[0], args[1:]
	switch {
	case cmd == "gen" && len(rest) > 0 && rest[0] == "secrets":
		return a.genSecrets(rest[1:])
	case cmd == "recovery-key":
		return a.recoveryKey(ctx, rest)
	case cmd == "sign":
		return runSign(rest)
	}
	return errUsage
}

// runNix runs nix with its errors on the terminal, where evaluation errors are most readable.
func runNix(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "nix", args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("nix %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func runSign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	imagePath := fs.String("image", "", "raw disk image to sign in place")
	repartJSON := fs.String("repart-json", "", "repart-output.json describing the image's partitions")
	key := fs.String("key", "", "PEM private key of the Secure Boot db signer")
	cert := fs.String("cert", "", "PEM certificate of the Secure Boot db signer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for _, f := range []struct{ name, value string }{
		{"--image", *imagePath}, {"--repart-json", *repartJSON}, {"--key", *key}, {"--cert", *cert},
	} {
		if f.value == "" {
			return errors.New("sign: " + f.name + " is required")
		}
	}
	return signImage(context.Background(), *imagePath, *repartJSON, *key, *cert)
}

func signImage(ctx context.Context, imagePath, repartJSON, key, cert string) error {
	parts, err := image.ReadPartitions(repartJSON)
	if err != nil {
		return err
	}
	esp, err := image.FindPartition(parts, "esp")
	if err != nil {
		return err
	}
	return imagesign.SignImage(ctx, imagePath, esp.Offset, key, cert)
}
