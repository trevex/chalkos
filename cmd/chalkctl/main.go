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
  install <node>                                install a node in maintenance mode
  disks (<node> | --endpoint ADDR)              list a node's disks
  apply-identity <node>                         deliver a node's identity from the cluster definition
  storage reset <node> <volume>                 wipe and recreate one volume
  status <node>                                 show an installed node's status
  logs <node> [-f] [--unit U]                   show a node's journal
  reboot <node>                                 reboot a node
  bootstrap <node>                              initialise the cluster on a control-plane node
  kubeconfig [--ttl 8760h] [--out FILE]         write an admin kubeconfig
  etcd members [--via NODE]                     list etcd's members and their health
  etcd remove-member <node|id> [--via NODE] [--force]
                                                remove a node's etcd member, such as a stale one
  etcd leave <node> [--force]                   take a control-plane node out of etcd
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
	// readSecret asks for a secret on the terminal without echoing it.
	readSecret func(ctx context.Context, prompt string) ([]byte, error)
}

func main() {
	home, _ := os.UserHomeDir()
	a := &app{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, nix: runNix, home: home, readSecret: ttySecret}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := a.run(ctx, os.Args[1:])
	status := exitStatus(err)
	// A command Ctrl-C ended exits as after SIGINT, also where the error lost errInterrupted on
	// the way, as age's error for an SSH key's passphrase does.
	if err != nil && ctx.Err() != nil {
		status = 130
	}
	stop()
	switch status {
	case 0:
	case 2:
		fmt.Fprintln(os.Stderr, usage)
	default:
		fmt.Fprintln(os.Stderr, "chalkctl:", err)
	}
	os.Exit(status)
}

var errUsage = errors.New("usage")

// exitStatus is chalkctl's exit status after err: 2 after a usage error and 130, as after
// SIGINT, when a prompt was interrupted.
func exitStatus(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage):
		return 2
	case errors.Is(err, errInterrupted):
		return 130
	}
	return 1
}

func (a *app) run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	cmd, rest := args[0], args[1:]
	switch {
	case cmd == "gen" && len(rest) > 0 && rest[0] == "secrets":
		return a.genSecrets(rest[1:])
	case cmd == "bootstrap":
		return a.bootstrap(ctx, rest)
	case cmd == "kubeconfig":
		return a.kubeconfig(ctx, rest)
	case cmd == "etcd":
		return a.etcd(ctx, rest)
	case cmd == "recovery-key":
		return a.recoveryKey(ctx, rest)
	case cmd == "install":
		return a.install(ctx, rest)
	case cmd == "disks":
		return a.disks(ctx, rest)
	case cmd == "apply-identity":
		return a.applyIdentity(ctx, rest)
	case cmd == "storage" && len(rest) > 0 && rest[0] == "reset":
		return a.resetVolume(ctx, rest[1:])
	case cmd == "status":
		return a.status(ctx, rest)
	case cmd == "logs":
		return a.logs(ctx, rest)
	case cmd == "reboot":
		return a.reboot(ctx, rest)
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
