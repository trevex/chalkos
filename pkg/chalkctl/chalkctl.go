// Package chalkctl is the chalkctl command, which installs and operates chalkos clusters.
// NewCommand returns its command tree, which programs can run or add to their own.
package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// app is chalkctl's environment, which every command shares. Its streams are those of the
// command that runs, so tests and programs embedding chalkctl capture them.
type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	// home is the user's home directory, where age and SSH keys are looked up.
	home string
	// readSecret asks for a secret on the terminal without echoing it.
	readSecret func(ctx context.Context, prompt string) ([]byte, error)
	// clock is the time; nil is the system's. pollInterval is the time between two looks at a
	// node waited for; zero is three seconds. Tests set both.
	clock        func() time.Time
	pollInterval time.Duration
}

// NewCommand returns chalkctl's root command, with age and SSH keys from the user's home directory
// and secrets asked for on the terminal.
func NewCommand() *cobra.Command {
	home, _ := os.UserHomeDir()
	return newCommand(&app{home: home, readSecret: ttySecret})
}

// with returns the environment with the streams of cmd.
func (a *app) with(cmd *cobra.Command) *app {
	c := *a
	c.stdin, c.stdout, c.stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	return &c
}

func newCommand(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:           "chalkctl",
		Short:         "Install and operate chalkos clusters",
		Long:          rootLong,
		Example:       rootExample,
		SilenceErrors: true,
		SilenceUsage:  true,
		// A flag before a subcommand's name is parsed as the flag of the command before it, which
		// has none: a usage error.
		TraverseChildren: true,
	}
	group(root)
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		// On the root or a group, the flag may as well be a misplaced subcommand's: a usage error.
		if cmd.HasSubCommands() {
			return fmt.Errorf("%w: %w", errUsage, err)
		}
		return fmt.Errorf("%w; %s --help lists its flags", err, cmd.CommandPath())
	})

	gen := group(&cobra.Command{Use: "gen", Short: "Generate files of a cluster", Long: genLong, Example: genExample})
	gen.AddCommand(a.genSecretsCommand())
	node := group(&cobra.Command{Use: "node", Short: "Manage a node's certificate", Long: nodeLong, Example: nodeExample})
	node.AddCommand(a.nodeRenewCommand(), a.nodeRevokeCommand(), a.nodeUnrevokeCommand())
	nodeCA := group(&cobra.Command{Use: "node-ca", Short: "Manage the node CA", Long: nodeCALong, Example: nodeCAExample})
	nodeCA.AddCommand(a.nodeCARotateCommand())
	config := group(&cobra.Command{Use: "config", Short: "Manage client files", Long: configLong, Example: configExample})
	config.AddCommand(a.configNewCommand())
	storage := group(&cobra.Command{Use: "storage", Short: "Manage a node's volumes", Long: storageLong, Example: storageExample})
	storage.AddCommand(a.resetVolumeCommand())
	etcd := group(&cobra.Command{Use: "etcd", Short: "Manage the cluster's etcd members", Long: etcdLong, Example: etcdExample})
	etcd.AddCommand(a.etcdMembersCommand(), a.etcdRemoveMemberCommand(), a.etcdLeaveCommand())
	root.AddCommand(
		gen, node, nodeCA, config, storage, etcd,
		a.recoveryKeyCommand(),
		a.installCommand(),
		a.disksCommand(),
		a.applyConfigCommand(),
		a.statusCommand(),
		a.logsCommand(),
		a.rebootCommand(),
		a.rotateCommand(),
		a.bootstrapCommand(),
		a.kubeconfigCommand(),
		a.upgradeCommand(),
		a.validateCommand(),
	)
	return root
}

// command makes cmd run run with the environment and streams of the command that runs.
func (a *app) command(cmd *cobra.Command, run func(a *app, ctx context.Context, args []string) error) *cobra.Command {
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return run(a.with(c), c.Context(), args)
	}
	return cmd
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
	// The root and groups fail only with usage errors; another error of theirs is a flag before a
	// subcommand's name, which cobra returns without the flag error function.
	if err != nil && !errors.Is(err, errUsage) && cmd.HasSubCommands() {
		err = fmt.Errorf("%w: %w", errUsage, err)
	}
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

// count is n and the noun, in the plural unless n is one.
func count[N ~int | ~uint32 | ~uint64](n N, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
