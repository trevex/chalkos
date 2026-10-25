package chalkctl

import (
	"context"
	"fmt"
	"math"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/diagnostics"
	"github.com/trevex/chalkos/pkg/internal/cli"
)

func (a *app) nodeDiagCommand() *cobra.Command {
	var f nodePinFlags
	cmd := a.command(&cobra.Command{
		Use:     "diag <node> [<diagnostic> [<parameter>=<value>...]]",
		Short:   "List or run a node's diagnostics",
		Long:    nodeDiagLong,
		Example: nodeDiagExample,
		Args:    cli.RangeArgs(1, math.MaxInt),
	}, func(a *app, ctx context.Context, pos []string) error { return a.nodeDiag(ctx, f, pos) })
	f.node.registerClient(cmd.Flags())
	f.pin.register(cmd.Flags())
	return cmd
}

func (a *app) nodeDiag(ctx context.Context, f nodePinFlags, pos []string) error {
	var args map[string]string
	if len(pos) > 1 {
		var err error
		if args, err = diagnosticArguments(pos[2:]); err != nil {
			return err
		}
	}
	t, err := a.clientTarget(ctx, f.node, pos[0])
	if err != nil {
		return err
	}
	conn, err := a.dialEither(t, f.pin)
	if err != nil {
		return err
	}
	defer conn.Close()
	if len(pos) == 1 {
		return a.listDiagnostics(ctx, conn)
	}
	return a.runDiagnostic(ctx, conn, pos[1], args)
}

// diagnosticArguments are a diagnostic's arguments, each given as <parameter>=<value>.
func diagnosticArguments(words []string) (map[string]string, error) {
	args := map[string]string{}
	for _, w := range words {
		name, value, ok := strings.Cut(w, "=")
		if !ok || name == "" {
			return nil, cli.Usagef("%q is no <parameter>=<value>", w)
		}
		if _, dup := args[name]; dup {
			return nil, cli.Usagef("%s is given twice", name)
		}
		args[name] = value
	}
	return args, nil
}

func (a *app) listDiagnostics(ctx context.Context, conn *client.Conn) error {
	resp, err := conn.Diagnostics(ctx, connect.NewRequest(&nodev1.DiagnosticsRequest{}))
	if err != nil {
		return err
	}
	w := a.table()
	fmt.Fprintln(w, "DIAGNOSTIC\tROLE\tDESCRIPTION")
	for _, d := range resp.Msg.Diagnostics {
		fmt.Fprintf(w, "%s\t%s\t%s\n", d.Name, d.Role, d.Description)
		for _, p := range d.Parameters {
			fmt.Fprintf(w, "  %s\t\t%s\n", parameterUsage(p), parameterNotes(p))
		}
	}
	return w.Flush()
}

// parameterUsage is how a parameter is given, <name>=<value>, in brackets when a call may leave
// it out; the value is an int's range or the parameter's type.
func parameterUsage(p *nodev1.DiagnosticParameter) string {
	value := p.Type
	if p.Type == "int" && p.Min != nil && p.Max != nil {
		value = fmt.Sprintf("%d-%d", *p.Min, *p.Max)
	}
	usage := p.Name + "=<" + value + ">"
	if p.Optional {
		usage = "[" + usage + "]"
	}
	return usage
}

// parameterNotes is a parameter's description, with an enum's values, a pattern's regex and
// the default.
func parameterNotes(p *nodev1.DiagnosticParameter) string {
	notes := p.Description
	if len(p.Values) > 0 {
		notes += " One of " + strings.Join(p.Values, ", ") + "."
	}
	if p.Regex != "" {
		notes += " Matches " + p.Regex + "."
	}
	if p.DefaultValue != "" {
		notes += " Default: " + p.DefaultValue + "."
	}
	return notes
}

func (a *app) runDiagnostic(ctx context.Context, conn *client.Conn, name string, args map[string]string) error {
	stream, err := conn.RunDiagnostic(ctx, connect.NewRequest(&nodev1.RunDiagnosticRequest{Name: name, Arguments: args}))
	if err != nil {
		return err
	}
	// Closing the stream early ends the call, and the node stops the run.
	defer stream.Close()
	var exit *nodev1.DiagnosticExit
	for stream.Receive() {
		var err error
		switch o := stream.Msg().Output.(type) {
		case *nodev1.RunDiagnosticResponse_Stdout:
			_, err = a.stdout.Write(o.Stdout)
		case *nodev1.RunDiagnosticResponse_Stderr:
			_, err = a.stderr.Write(o.Stderr)
		case *nodev1.RunDiagnosticResponse_Exit:
			exit = o.Exit
		}
		if err != nil {
			return fmt.Errorf("print what %s prints: %w", name, err)
		}
	}
	if err := stream.Err(); err != nil {
		return err
	}
	switch {
	case exit == nil:
		return fmt.Errorf("the node ended %s without saying how it ended", name)
	case exit.Stopped == diagnostics.StoppedTimeout:
		return fmt.Errorf("%s ran longer than its timeout, and the node stopped it", name)
	case exit.Stopped == diagnostics.StoppedOutputLimit:
		return fmt.Errorf("%s printed more than 1 MiB, and the node stopped it there", name)
	case exit.Stopped != "":
		return fmt.Errorf("%s ended: %s", name, exit.Stopped)
	case exit.Code != 0:
		return fmt.Errorf("%s exited with status %d", name, exit.Code)
	}
	return nil
}
