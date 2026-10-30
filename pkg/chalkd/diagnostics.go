package chalkd

import (
	"context"
	"io"
	"maps"
	"slices"
	"sync"

	"connectrpc.com/connect"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/diagnostics"
	"github.com/trevex/chalkos/pkg/pki"
)

func (s *Server) Diagnostics(ctx context.Context, _ *connect.Request[nodev1.DiagnosticsRequest]) (*connect.Response[nodev1.DiagnosticsResponse], error) {
	defs, err := diagnostics.Read(s.Paths.Diagnostics)
	if err != nil {
		return nil, failed(connect.CodeInternal, "read the image's diagnostics: %v", err)
	}
	resp := &nodev1.DiagnosticsResponse{}
	for _, name := range slices.Sorted(maps.Keys(defs)) {
		resp.Diagnostics = append(resp.Diagnostics, diagnosticOf(name, defs[name]))
	}
	return connect.NewResponse(resp), nil
}

// diagnosticOf is a diagnostic as Diagnostics lists it.
func diagnosticOf(name string, d diagnostics.Definition) *nodev1.Diagnostic {
	pb := &nodev1.Diagnostic{Name: name, Description: d.Description, Role: d.Role, Timeout: d.Timeout}
	for _, n := range d.ParameterNames() {
		p := d.Parameters[n]
		pb.Parameters = append(pb.Parameters, &nodev1.DiagnosticParameter{
			Name:         n,
			Type:         p.Type,
			Description:  p.Description,
			Optional:     !p.Required(),
			DefaultValue: p.Default,
			Min:          p.Min,
			Max:          p.Max,
			Values:       p.Values,
			Regex:        p.Regex,
		})
	}
	return pb
}

func (s *Server) RunDiagnostic(ctx context.Context, req *connect.Request[nodev1.RunDiagnosticRequest], stream *connect.ServerStream[nodev1.RunDiagnosticResponse]) error {
	name := req.Msg.Name
	defs, err := diagnostics.Read(s.Paths.Diagnostics)
	if err != nil {
		return failed(connect.CodeInternal, "read the image's diagnostics: %v", err)
	}
	d, ok := defs[name]
	if !ok {
		return failed(connect.CodeNotFound, "the image declares no diagnostic %q; chalkctl node diag lists those it does", name)
	}
	if role, _ := ctx.Value(roleKey{}).(pki.Role); !role.Allows(pki.Role(d.Role)) {
		return failed(connect.CodePermissionDenied, "the diagnostic %s needs the %s role", name, d.Role)
	}
	argv, err := d.Argv(req.Msg.Arguments)
	if err != nil {
		return failed(connect.CodeInvalidArgument, "diagnostic %s: %v", name, err)
	}
	ns, err := d.Namespace(req.Msg.Arguments)
	if err != nil {
		return failed(connect.CodeInvalidArgument, "diagnostic %s: %v", name, err)
	}
	if s.diagnosticRuns.Add(1) > maxDiagnosticRuns {
		s.diagnosticRuns.Add(-1)
		return failed(connect.CodeResourceExhausted, "%d diagnostics run already; wait for one to end", maxDiagnosticRuns)
	}
	defer s.diagnosticRuns.Add(-1)

	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	out := &diagnosticOutput{stream: stream, stop: stop, left: diagnostics.MaxOutput}
	run := diagnostics.Run{Name: name, Argv: argv, Capabilities: d.Capabilities, Groups: d.Groups, NetworkNamespace: ns, Timeout: d.RunTime()}
	exit, err := s.diagnose().Run(runCtx, run, out.writer(false), out.writer(true))
	return out.finish(ctx, name, exit, err)
}

func (s *Server) diagnose() diagnostics.Runner {
	if s.Diagnose != nil {
		return s.Diagnose
	}
	return diagnostics.Sandbox{}
}

// diagnosticOutput sends what a run prints as messages of the stream, from the goroutines that
// read its standard output and error, up to diagnostics.MaxOutput bytes together. Once the output
// passes that, or a send fails, it stops the run and discards what the run prints until it ends,
// so that its pipes never fill. A run may return while its output is still being copied, so the
// stream's last message is sent under the same lock, and what is written after it is discarded.
type diagnosticOutput struct {
	stream *connect.ServerStream[nodev1.RunDiagnosticResponse]
	stop   context.CancelFunc

	mu      sync.Mutex
	left    int
	limited bool
	done    bool
	err     error
}

func (o *diagnosticOutput) writer(stderr bool) io.Writer {
	return writerFunc(func(p []byte) (int, error) {
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.done || o.limited || o.err != nil {
			return len(p), nil
		}
		n := min(len(p), o.left)
		if n > 0 {
			msg := &nodev1.RunDiagnosticResponse{Output: &nodev1.RunDiagnosticResponse_Stdout{Stdout: p[:n]}}
			if stderr {
				msg.Output = &nodev1.RunDiagnosticResponse_Stderr{Stderr: p[:n]}
			}
			if err := o.stream.Send(msg); err != nil {
				o.err = err
				o.stop()
				return len(p), nil
			}
			o.left -= n
		}
		if n < len(p) {
			o.limited = true
			o.stop()
		}
		return len(p), nil
	})
}

// finish ends the output once the run returned, with how the run ended: the stream's last
// message, or the error the call fails with.
func (o *diagnosticOutput) finish(ctx context.Context, name string, exit diagnostics.Exit, err error) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.done = true
	switch {
	case o.err != nil:
		return o.err
	case o.limited:
		// The run returns its context's error once stopped, or how it ended if it ended first.
		exit = diagnostics.Exit{Stopped: diagnostics.StoppedOutputLimit}
	case ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		return failed(connect.CodeInternal, "run the diagnostic %s: %v", name, err)
	}
	return o.stream.Send(&nodev1.RunDiagnosticResponse{Output: &nodev1.RunDiagnosticResponse_Exit{
		Exit: &nodev1.DiagnosticExit{Code: int32(exit.Code), Stopped: exit.Stopped},
	}})
}

// writerFunc is a function as an io.Writer.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
