// Package chalkctl is the chalkctl command, which builds, signs, installs and operates chalkos
// clusters. NewCommand returns its command tree, which programs can run or add to their own.
package chalkctl

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/image"
	"github.com/trevex/chalkos/pkg/imagesign"
)

// app is chalkctl's environment, which every command shares. Its streams are those of the
// command that runs, so tests and programs embedding chalkctl capture them.
type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	// nix runs nix and returns its standard output.
	nix func(ctx context.Context, args ...string) ([]byte, error)
	// home is the user's home directory, where age and SSH keys are looked up.
	home string
	// readSecret asks for a secret on the terminal without echoing it.
	readSecret func(ctx context.Context, prompt string) ([]byte, error)
	// clock is the time; nil is the system's. pollInterval is the time between two looks at a
	// node waited for; zero is three seconds. Tests set both.
	clock        func() time.Time
	pollInterval time.Duration
}

// NewCommand returns chalkctl's root command, with nix from PATH, age and SSH keys from the
// user's home directory and secrets asked for on the terminal.
func NewCommand() *cobra.Command {
	home, _ := os.UserHomeDir()
	return newCommand(&app{nix: runNix, home: home, readSecret: ttySecret})
}

// with returns the environment with the streams of cmd.
func (a *app) with(cmd *cobra.Command) *app {
	c := *a
	c.stdin, c.stdout, c.stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	return &c
}

func newCommand(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "chalkctl",
		Short: "Build, sign, install and operate chalkos clusters",
		Long: `chalkctl builds, signs, installs and operates the nodes of a chalkos cluster. It reads the
cluster definition from a flake (or a manifest file), authenticates with the cluster's secrets
file or a client file, and talks to chalkd on each node.`,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	group(root)
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fmt.Errorf("%w; %s --help lists its flags", err, cmd.CommandPath())
	})

	gen := group(&cobra.Command{Use: "gen", Short: "Generate files of a cluster"})
	gen.AddCommand(legacy(a, "secrets", "Generate the cluster's secrets file", func(a *app, _ context.Context, args []string) error { return a.genSecrets(args) }))
	node := group(&cobra.Command{Use: "node", Short: "Manage a node's certificate"})
	node.AddCommand(legacy(a, "renew <node>", "Issue a node a new node certificate, also once its own expired", (*app).nodeRenew))
	nodeCA := group(&cobra.Command{Use: "node-ca", Short: "Manage the node CA"})
	nodeCA.AddCommand(legacy(a, "rotate", "Issue a new node CA and deliver it to the control-plane nodes", (*app).nodeCARotate))
	config := group(&cobra.Command{Use: "config", Short: "Manage client files"})
	config.AddCommand(legacy(a, "new", "Write a client file, which operates the cluster without the secrets file", (*app).configNew))
	storage := group(&cobra.Command{Use: "storage", Short: "Manage a node's volumes"})
	storage.AddCommand(legacy(a, "reset <node> <volume>", "Wipe and recreate one volume", (*app).resetVolume))
	etcd := group(&cobra.Command{Use: "etcd", Short: "Manage the cluster's etcd members"})
	etcd.AddCommand(
		legacy(a, "members", "List etcd's members and their health", (*app).etcdMembers),
		legacy(a, "remove-member <node|id>", "Remove a node's etcd member, such as a stale one", (*app).etcdRemoveMember),
		legacy(a, "leave <node>", "Take a control-plane node out of etcd", (*app).etcdLeave),
	)
	root.AddCommand(
		gen, node, nodeCA, config, storage, etcd,
		legacy(a, "recovery-key <node>", "Print a node's recovery key", (*app).recoveryKey),
		legacy(a, "install <node>", "Install a node in maintenance mode", (*app).install),
		legacy(a, "disks [<node>]", "List a node's disks", (*app).disks),
		legacy(a, "apply-identity <node>", "Deliver a node's identity from the cluster definition", (*app).applyIdentity),
		legacy(a, "status <node>", "Show an installed node's status", (*app).status),
		legacy(a, "logs <node>", "Show a node's journal", (*app).logs),
		legacy(a, "reboot <node>", "Reboot a node", (*app).reboot),
		legacy(a, "rotate <kind>", "Rotate os-ca, kubernetes-ca, service-account-key or encryption-key", (*app).rotate),
		legacy(a, "bootstrap <node>", "Initialise the cluster on a control-plane node", (*app).bootstrap),
		legacy(a, "kubeconfig", "Write an admin kubeconfig", (*app).kubeconfig),
		legacy(a, "upgrade", "Install new images on the cluster's nodes, one control plane at a time", (*app).upgrade),
		legacy(a, "sign", "Sign the boot loader and UKIs of a disk image", func(_ *app, ctx context.Context, args []string) error { return runSign(ctx, args) }),
	)
	return root
}

// legacy is a command whose handler parses its own flags.
func legacy(a *app, use, short string, run func(a *app, ctx context.Context, args []string) error) *cobra.Command {
	return &cobra.Command{
		Use:                use,
		Short:              short,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(a.with(cmd), cmd.Context(), args)
		},
	}
}

// group makes cmd a command that only holds subcommands: run alone, or with one it does not
// have, it is a usage error.
func group(cmd *cobra.Command) *cobra.Command {
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("%w: unknown command %q for %q", errUsage, args[0], cmd.CommandPath())
		}
		return nil
	}
	cmd.RunE = func(*cobra.Command, []string) error { return errUsage }
	return cmd
}

// Execute runs chalkctl's command tree with ctx, prints its error and returns the exit status:
// 2 and the command's usage after a usage error, 130 as after SIGINT when ctx ended, 1 after
// another error.
func Execute(ctx context.Context, root *cobra.Command) int {
	cmd, err := root.ExecuteContextC(ctx)
	status := exitStatus(err)
	// A command Ctrl-C ended exits as after SIGINT, also where the error lost errInterrupted on
	// the way, as age's error for an SSH key's passphrase does.
	if err != nil && ctx.Err() != nil {
		status = 130
	}
	switch {
	case status == 0:
	case status == 2:
		if err != errUsage {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", root.Name(), err)
		}
		fmt.Fprint(cmd.ErrOrStderr(), cmd.UsageString())
	default:
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", root.Name(), err)
	}
	return status
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

func runSign(ctx context.Context, args []string) error {
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
	return signImage(ctx, *imagePath, *repartJSON, *key, *cert)
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
