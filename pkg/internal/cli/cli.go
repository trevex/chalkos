// Package cli holds what chalkctl, chalkd, chalklab, chalkbuild and chalkcfg share around their
// cobra command trees: usage errors, the root and group commands, positional argument checks,
// enum flags and Execute, which prints a command's error and returns its exit status.
package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/trevex/chalkos/pkg/internal/strs"
)

// ErrUsage is the error of a command line the command tree does not take. Execute prints the
// command's usage after it and exits with 2.
var ErrUsage = errors.New("usage")

// Usagef is a usage error with the formatted message.
func Usagef(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrUsage}, args...)...)
}

// usage wraps a non-nil err in ErrUsage.
func usage(err error) error {
	if err == nil || errors.Is(err, ErrUsage) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrUsage, err)
}

// Root makes root the root of a command tree that Execute runs:
//   - Execute prints errors and the usage, so cobra prints neither.
//   - Every flag error of the tree is a usage error.
//   - root only holds subcommands, as a Group does.
//   - A flag given before a subcommand's name that a command before it takes is a usage error:
//     with TraverseChildren, cobra parses it as that command's, and the subcommand never sees it.
func Root(root *cobra.Command) *cobra.Command {
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usage(err) })
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		for p := cmd.Parent(); p != nil; p = p.Parent() {
			var err error
			p.Flags().Visit(func(f *pflag.Flag) {
				if err == nil && p.LocalNonPersistentFlags().Lookup(f.Name) != nil {
					err = Usagef("--%s goes after %s", f.Name, cmd.CommandPath())
				}
			})
			if err != nil {
				return err
			}
		}
		return nil
	}
	return Group(root)
}

// Group makes cmd a command that only holds subcommands: run alone, or with one it does not have,
// it is a usage error.
func Group(cmd *cobra.Command) *cobra.Command {
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return Usagef("unknown command %q for %q", args[0], cmd.CommandPath())
		}
		return nil
	}
	cmd.RunE = func(*cobra.Command, []string) error { return ErrUsage }
	return cmd
}

// NoArgs refuses positional arguments with a usage error.
func NoArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 && !cmd.HasSubCommands() {
		return Usagef("%s takes no arguments", cmd.CommandPath())
	}
	return usage(cobra.NoArgs(cmd, args))
}

// ExactArgs refuses all but n positional arguments with a usage error.
func ExactArgs(n int) cobra.PositionalArgs {
	return wrap(cobra.ExactArgs(n))
}

// RangeArgs refuses fewer than min or more than max positional arguments with a usage error.
func RangeArgs(min, max int) cobra.PositionalArgs {
	return wrap(cobra.RangeArgs(min, max))
}

// MinimumNArgs refuses fewer than n positional arguments with a usage error.
func MinimumNArgs(n int) cobra.PositionalArgs {
	return wrap(cobra.MinimumNArgs(n))
}

func wrap(check cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error { return usage(check(cmd, args)) }
}

// Options change how Execute ends.
type Options struct {
	// Interruptible makes a command whose context ended exit with 130, as after SIGINT, whatever
	// its error.
	Interruptible bool
	// SplitJoined prints each error that an error other than a usage error joins on a line of its
	// own, after the root's name, as a log prints them.
	SplitJoined bool
}

// Execute runs the command tree of root with ctx, prints its error and returns the exit status:
// 0 after success, 2 and the command's usage after a usage error, 1 after another error, and with
// opts.Interruptible 130 after an error once ctx ended.
func Execute(ctx context.Context, root *cobra.Command, opts Options) int {
	cmd, err := root.ExecuteContextC(ctx)
	if err == nil {
		return 0
	}
	if isUsage(cmd, err) {
		err = usage(err)
	}
	status := 1
	switch {
	// A command Ctrl-C ended exits as after SIGINT, also where its error does not say so, as
	// age's error for an SSH key's passphrase does not.
	case opts.Interruptible && ctx.Err() != nil:
		status = 130
	case errors.Is(err, ErrUsage):
		status = 2
	}
	if err != ErrUsage {
		for _, e := range lines(err, opts.SplitJoined && status != 2) {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", root.Name(), e)
		}
	}
	if status == 2 {
		fmt.Fprint(cmd.ErrOrStderr(), cmd.UsageString())
	}
	return status
}

// isUsage reports whether cobra failed cmd before running it, with an error it returns without
// the flag error function:
//   - The root fails only with usage errors. Another error of its own is a flag before a
//     subcommand's name that the command before it does not take, which cobra returns with the
//     root.
//   - A command with a required flag not set, or with flags of a group set wrongly, never runs.
//   - A command whose Args refuses its positional arguments never runs.
func isUsage(cmd *cobra.Command, err error) bool {
	if errors.Is(err, ErrUsage) {
		return false
	}
	return !cmd.HasParent() || cmd.ValidateRequiredFlags() != nil || cmd.ValidateFlagGroups() != nil ||
		cmd.ValidateArgs(cmd.Flags().Args()) != nil
}

// lines are the errors err joins with split, or err alone.
func lines(err error, split bool) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok && split {
		return j.Unwrap()
	}
	return []error{err}
}

// Enum is the value of a flag that takes one of allowed, which its usage calls a typ.
func Enum[T ~string](p *T, typ string, allowed ...T) pflag.Value {
	return &enum[T]{p, typ, allowed}
}

type enum[T ~string] struct {
	p       *T
	typ     string
	allowed []T
}

func (e *enum[T]) String() string { return string(*e.p) }
func (e *enum[T]) Type() string   { return e.typ }

func (e *enum[T]) Set(v string) error {
	if !slices.Contains(e.allowed, T(v)) {
		names := make([]string, len(e.allowed))
		for i, a := range e.allowed {
			names[i] = string(a)
		}
		return errors.New("want " + strs.Or(names))
	}
	*e.p = T(v)
	return nil
}
