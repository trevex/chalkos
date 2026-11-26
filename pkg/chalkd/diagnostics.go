package chalkd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sync"

	"connectrpc.com/connect/v2"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/api/node/v1/nodev1connect"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/diagnostics"
	"github.com/trevex/chalkos/pkg/pki"
)

func (s *Server) Diagnostics(ctx context.Context, _ *nodev1.DiagnosticsRequest) (*nodev1.DiagnosticsResponse, error) {
	defs := s.Loader.Contract.Diagnostics
	resp := &nodev1.DiagnosticsResponse{}
	for _, name := range slices.Sorted(maps.Keys(defs)) {
		resp.Diagnostics = append(resp.Diagnostics, diagnosticOf(name, defs[name]))
	}
	return resp, nil
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

func (s *Server) RunDiagnostic(ctx context.Context, req *nodev1.RunDiagnosticRequest, stream nodev1connect.NodeServiceRunDiagnosticServerStream) error {
	return s.runDiagnostic(ctx, req, stream)
}

// diagnosticStream is the stream of RunDiagnostic's responses.
type diagnosticStream interface {
	Send(*nodev1.RunDiagnosticResponse) error
}

// runDiagnostic is RunDiagnostic with its responses sent to stream.
func (s *Server) runDiagnostic(ctx context.Context, req *nodev1.RunDiagnosticRequest, stream diagnosticStream) error {
	name := req.Name
	d, ok := s.Loader.Contract.Diagnostics[name]
	if !ok {
		return layer.Errorf(connect.CodeNotFound, "the image declares no diagnostic %q; chalkctl node diag lists those it does", name)
	}
	if !s.diagnosticAllowed(ctx, pki.Role(d.Role)) {
		return layer.Errorf(connect.CodePermissionDenied, "the diagnostic %s needs the %s role", name, d.Role)
	}
	argv, ns, err := d.Call(req.Arguments)
	if err != nil {
		return layer.Errorf(connect.CodeInvalidArgument, "diagnostic %s: %w", name, err)
	}
	if s.diagnosticRuns.Add(1) > maxDiagnosticRuns {
		s.diagnosticRuns.Add(-1)
		return layer.Errorf(connect.CodeResourceExhausted, "%d diagnostics run already; wait for one to end", maxDiagnosticRuns)
	}
	// The run's slot is free once its unit ended: a client that stops reading blocks the stream's
	// last sends, but holds no slot.
	release := sync.OnceFunc(func() { s.diagnosticRuns.Add(-1) })
	defer release()

	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	out := newDiagnosticOutput(stream, stop)
	run := diagnostics.Run{Name: name, Argv: argv, Capabilities: d.Capabilities, Groups: d.Groups, NetworkNamespace: ns, Timeout: d.RunTime()}
	exit, err := s.diagnose().Run(runCtx, run, out.writer(false), out.writer(true))
	release()
	return out.finish(ctx, name, ns, exit, err)
}

// diagnosticAllowed reports whether the caller may run a diagnostic that needs the role need. The
// console runs those of readers, and those of operators only with ConsoleActions, which lets it
// reboot the node too.
func (s *Server) diagnosticAllowed(ctx context.Context, need pki.Role) bool {
	if onConsole(ctx) {
		console := pki.RoleReader
		if s.ConsoleActions {
			console = pki.RoleOperator
		}
		return console.Allows(need)
	}
	role, _ := ctx.Value(roleKey{}).(pki.Role)
	return role.Allows(need)
}

func (s *Server) diagnose() diagnostics.Runner {
	if s.Diagnose != nil {
		return s.Diagnose
	}
	return diagnostics.Sandbox{}
}

// diagnosticOutput sends what a run prints as messages of the stream, up to diagnostics.MaxOutput
// bytes together. The goroutines that read the run's standard output and error queue it, and send
// sends the queue, so a client that stops reading blocks neither the run's end nor its pipes. Once
// the output passes the limit, or a send fails, it stops the run and discards what the run prints
// until it ends. What is written once the run returned is discarded.
type diagnosticOutput struct {
	stream diagnosticStream
	stop   context.CancelFunc
	// sent is closed once send returned, having sent the queue or failed.
	sent chan struct{}

	mu      sync.Mutex
	more    *sync.Cond
	queue   []*nodev1.RunDiagnosticResponse
	left    int
	limited bool
	done    bool
	err     error
}

func newDiagnosticOutput(stream diagnosticStream, stop context.CancelFunc) *diagnosticOutput {
	o := &diagnosticOutput{stream: stream, stop: stop, sent: make(chan struct{}), left: diagnostics.MaxOutput}
	o.more = sync.NewCond(&o.mu)
	go o.send()
	return o
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
			data := bytes.Clone(p[:n])
			msg := &nodev1.RunDiagnosticResponse{Output: &nodev1.RunDiagnosticResponse_Stdout{Stdout: data}}
			if stderr {
				msg.Output = &nodev1.RunDiagnosticResponse_Stderr{Stderr: data}
			}
			o.queue = append(o.queue, msg)
			o.more.Signal()
			o.left -= n
		}
		if n < len(p) {
			o.limited = true
			o.stop()
		}
		return len(p), nil
	})
}

// send sends the queued output until the run returned and the queue is empty, or a send failed.
func (o *diagnosticOutput) send() {
	defer close(o.sent)
	o.mu.Lock()
	defer o.mu.Unlock()
	for {
		for len(o.queue) == 0 && !o.done {
			o.more.Wait()
		}
		if len(o.queue) == 0 {
			return
		}
		msg := o.queue[0]
		o.queue = o.queue[1:]
		o.mu.Unlock()
		err := o.stream.Send(msg)
		o.mu.Lock()
		if err != nil {
			o.err = err
			o.queue = nil
			o.stop()
			return
		}
	}
}

// finish ends the output once the run of the diagnostic name in the network namespace ns
// returned, with how the run ended: the stream's last message, sent after the queued output, or
// the error the call fails with.
func (o *diagnosticOutput) finish(ctx context.Context, name, ns string, exit diagnostics.Exit, err error) error {
	o.mu.Lock()
	o.done = true
	o.more.Signal()
	o.mu.Unlock()
	<-o.sent
	o.mu.Lock()
	defer o.mu.Unlock()
	switch {
	case o.err != nil:
		return o.err
	case o.limited:
		// The run returns its context's error once stopped, or how it ended if it ended first.
		exit = diagnostics.Exit{Stopped: diagnostics.StoppedOutputLimit}
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, diagnostics.ErrNoNamespace):
		return layer.Errorf(connect.CodeFailedPrecondition, "diagnostic %s: the network namespace %s does not exist", name, ns)
	case err != nil:
		return fmt.Errorf("run the diagnostic %s: %w", name, err)
	}
	return o.stream.Send(&nodev1.RunDiagnosticResponse{Output: &nodev1.RunDiagnosticResponse_Exit{
		Exit: &nodev1.DiagnosticExit{Code: int32(exit.Code), Stopped: exit.Stopped},
	}})
}

// writerFunc is a function as an io.Writer.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
