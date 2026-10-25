// Package chalkctl is the chalkctl command, which installs and operates chalkos clusters.
// NewCommand returns its command tree, which programs can run or add to their own.
package chalkctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/internal/cli"
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
	root := cli.Root(&cobra.Command{
		Use:     "chalkctl",
		Short:   "Install and operate chalkos clusters",
		Long:    rootLong,
		Example: rootExample,
		// A flag before a subcommand's name is parsed as the flag of the command before it, which
		// has none: a usage error.
		TraverseChildren: true,
	})

	gen := cli.Group(&cobra.Command{Use: "gen", Short: "Generate files of a cluster", Long: genLong, Example: genExample})
	gen.AddCommand(a.genSecretsCommand())
	node := cli.Group(&cobra.Command{Use: "node", Short: "Manage a node's access and run its diagnostics", Long: nodeLong, Example: nodeExample})
	node.AddCommand(a.nodeRenewCommand(), a.nodeRevokeCommand(), a.nodeUnrevokeCommand(), a.nodeDiagCommand())
	nodeCA := cli.Group(&cobra.Command{Use: "node-ca", Short: "Manage the node CA", Long: nodeCALong, Example: nodeCAExample})
	nodeCA.AddCommand(a.nodeCARotateCommand())
	config := cli.Group(&cobra.Command{Use: "config", Short: "Manage client files", Long: configLong, Example: configExample})
	config.AddCommand(a.configNewCommand())
	storage := cli.Group(&cobra.Command{Use: "storage", Short: "Manage a node's volumes", Long: storageLong, Example: storageExample})
	storage.AddCommand(a.resetVolumeCommand())
	etcd := cli.Group(&cobra.Command{Use: "etcd", Short: "Manage the cluster's etcd members", Long: etcdLong, Example: etcdExample})
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

// Execute runs chalkctl's command tree with ctx, prints its error and returns the exit status:
// 2 and the command's usage after a usage error, 130 as after SIGINT when ctx ended, 1 after
// another error.
func Execute(ctx context.Context, root *cobra.Command) int {
	return cli.Execute(ctx, root, cli.Options{Interruptible: true})
}

// count is n and the noun, in the plural unless n is one.
func count[N ~int | ~uint32 | ~uint64](n N, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// poll is the time between two looks at the nodes' status.
func (a *app) poll() time.Duration {
	if a.pollInterval > 0 {
		return a.pollInterval
	}
	return 3 * time.Second
}

// now is chalkctl's clock.
func (a *app) now() time.Time {
	if a.clock != nil {
		return a.clock()
	}
	return time.Now()
}

// waitFor runs check every interval until it passes, up to timeout. A check that fails with a
// permanent error ends the wait at once with the error it wraps. Once the time is up, the error
// is the last check's; once ctx ended, it is the context's cause. A what other than "" names what
// was waited for in either: "waiting for <what>: ...".
func waitFor(ctx context.Context, what string, timeout, interval time.Duration, check func() error) error {
	deadline := time.Now().Add(timeout)
	for {
		err := check()
		var p permanent
		switch {
		case err == nil:
			return nil
		case errors.As(err, &p):
			return p.error
		case time.Now().Before(deadline):
			select {
			case <-ctx.Done():
				err = context.Cause(ctx)
			case <-time.After(interval):
				continue
			}
		}
		if what == "" {
			return err
		}
		return fmt.Errorf("waiting for %s: %w", what, err)
	}
}

// permanent is a check's error that waiting does not change.
type permanent struct{ error }

func (p permanent) Unwrap() error { return p.error }

// table writes aligned columns, separated by tabs, to standard output once flushed.
func (a *app) table() *tabwriter.Writer {
	return tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
}
