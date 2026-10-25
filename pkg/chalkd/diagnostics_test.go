package chalkd

import (
	"context"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/diagnostics"
	"github.com/trevex/chalkos/pkg/pki"
)

// testDiagnostics declares route-get, which readers run, and ping, which needs an operator and
// gets a capability.
const testDiagnostics = `{
  "route-get": {"description": "The route the kernel takes to an address.",
    "command": ["/nix/store/x-iproute2/bin/ip", "-json", "route", "get", "{address}"],
    "parameters": {"address": {"type": "address", "description": "IPv4 or IPv6 address."}},
    "role": "reader", "timeout": "10s"},
  "ping": {"description": "Pings an address.",
    "command": ["/nix/store/x-iputils/bin/ping", "-c", "{count}", "{address}"],
    "parameters": {"address": {"type": "address", "description": "Address to ping."},
      "count": {"type": "int", "description": "Echo requests.", "min": 1, "max": 10, "default": "4"}},
    "role": "operator", "timeout": "20s", "capabilities": ["CAP_NET_RAW"]}}`

// fakeDiagnose records each run and answers it with the output and exit given, writing the
// output in chunks as a program prints. A run whose context ends returns its error, as the
// sandbox's does once chalkd stops it, and counts as stopped.
type fakeDiagnose struct {
	mu             sync.Mutex
	runs           []diagnostics.Run
	stopped        int
	stdout, stderr string
	exit           diagnostics.Exit
	err            error
}

func (f *fakeDiagnose) Run(ctx context.Context, r diagnostics.Run, stdout, stderr io.Writer) (diagnostics.Exit, error) {
	f.mu.Lock()
	f.runs = append(f.runs, r)
	f.mu.Unlock()
	for _, out := range []struct {
		w io.Writer
		s string
	}{{stdout, f.stdout}, {stderr, f.stderr}} {
		for s := out.s; len(s) > 0 && ctx.Err() == nil; {
			n := min(len(s), 32<<10)
			if _, err := io.WriteString(out.w, s[:n]); err != nil {
				return diagnostics.Exit{}, err
			}
			s = s[n:]
		}
	}
	if ctx.Err() != nil {
		f.mu.Lock()
		f.stopped++
		f.mu.Unlock()
		return diagnostics.Exit{}, ctx.Err()
	}
	return f.exit, f.err
}

func (f *fakeDiagnose) stops() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

func (f *fakeDiagnose) recorded() []diagnostics.Run {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.runs)
}

// diagnosticsNode is a node in normal mode that declares testDiagnostics and runs them with f,
// and a connection to it with the role given.
func diagnosticsNode(t *testing.T, f *fakeDiagnose, role pki.Role) (*Server, *client.Conn) {
	t.Helper()
	c := newCreds(t)
	s, _ := newTestServer(t, normal, vda)
	write(t, s.Paths.Diagnostics, testDiagnostics)
	s.Diagnose = f
	return s, dial(t, serve(t, s, c, c.pool), c.clients[role])
}

// runDiagnostic runs a diagnostic and returns what it printed and how it ended.
func runDiagnostic(conn *client.Conn, name string, args map[string]string) (stdout, stderr string, exit *nodev1.DiagnosticExit, err error) {
	stream, err := conn.RunDiagnostic(context.Background(), connect.NewRequest(&nodev1.RunDiagnosticRequest{Name: name, Arguments: args}))
	if err != nil {
		return "", "", nil, err
	}
	var out, errOut strings.Builder
	for stream.Receive() {
		switch o := stream.Msg().Output.(type) {
		case *nodev1.RunDiagnosticResponse_Stdout:
			out.Write(o.Stdout)
		case *nodev1.RunDiagnosticResponse_Stderr:
			errOut.Write(o.Stderr)
		case *nodev1.RunDiagnosticResponse_Exit:
			exit = o.Exit
		}
	}
	return out.String(), errOut.String(), exit, stream.Err()
}

// Diagnostics lists the image's diagnostics in name order, each parameter with what a call may
// give, the required ones first.
func TestDiagnosticsListsTheImagesDiagnostics(t *testing.T) {
	_, conn := diagnosticsNode(t, &fakeDiagnose{}, pki.RoleReader)
	resp, err := conn.Diagnostics(context.Background(), connect.NewRequest(&nodev1.DiagnosticsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	want := &nodev1.DiagnosticsResponse{Diagnostics: []*nodev1.Diagnostic{
		{Name: "ping", Description: "Pings an address.", Role: "operator", Timeout: "20s", Parameters: []*nodev1.DiagnosticParameter{
			{Name: "address", Type: "address", Description: "Address to ping."},
			{Name: "count", Type: "int", Description: "Echo requests.", Optional: true, DefaultValue: "4", Min: proto.Int64(1), Max: proto.Int64(10)},
		}},
		{Name: "route-get", Description: "The route the kernel takes to an address.", Role: "reader", Timeout: "10s", Parameters: []*nodev1.DiagnosticParameter{
			{Name: "address", Type: "address", Description: "IPv4 or IPv6 address."},
		}},
	}}
	if !proto.Equal(resp.Msg, want) {
		t.Errorf("Diagnostics = %v, want %v", resp.Msg, want)
	}
}

// RunDiagnostic runs the command line the arguments fill, with the diagnostic's capabilities and
// time, and streams what it prints, then how it ended.
func TestRunDiagnostic(t *testing.T) {
	f := &fakeDiagnose{stdout: `[{"dst":"2001:db8::1"}]` + "\n", stderr: "a warning\n", exit: diagnostics.Exit{Code: 0}}
	_, conn := diagnosticsNode(t, f, pki.RoleReader)
	stdout, stderr, exit, err := runDiagnostic(conn, "route-get", map[string]string{"address": "2001:0db8::1"})
	if err != nil || stdout != f.stdout || stderr != f.stderr || exit == nil || exit.Code != 0 || exit.Stopped != "" {
		t.Fatalf("route-get = %q, %q, %v, %v", stdout, stderr, exit, err)
	}
	want := []diagnostics.Run{{Name: "route-get", Argv: []string{"/nix/store/x-iproute2/bin/ip", "-json", "route", "get", "2001:db8::1"}, Timeout: 10 * time.Second}}
	if got := f.recorded(); !reflect.DeepEqual(got, want) {
		t.Errorf("runs = %+v, want %+v", got, want)
	}

	f = &fakeDiagnose{exit: diagnostics.Exit{Code: 1}}
	_, conn = diagnosticsNode(t, f, pki.RoleOperator)
	if _, _, exit, err := runDiagnostic(conn, "ping", map[string]string{"address": "192.0.2.1"}); err != nil || exit.GetCode() != 1 {
		t.Fatalf("ping = %v, %v", exit, err)
	}
	want = []diagnostics.Run{{Name: "ping", Argv: []string{"/nix/store/x-iputils/bin/ping", "-c", "4", "192.0.2.1"}, Capabilities: []string{"CAP_NET_RAW"}, Timeout: 20 * time.Second}}
	if got := f.recorded(); !reflect.DeepEqual(got, want) {
		t.Errorf("runs = %+v, want %+v", got, want)
	}
}

// RunDiagnostic refuses, before it runs anything, a diagnostic the image lacks, a caller without
// the diagnostic's role and arguments that do not fit its parameters.
func TestRunDiagnosticRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		role       pki.Role
		diagnostic string
		args       map[string]string
		code       connect.Code
		want       string
	}{
		"an unknown diagnostic": {pki.RoleReader, "nope", nil, connect.CodeNotFound, `the image declares no diagnostic "nope"`},
		"too little a role":     {pki.RoleReader, "ping", map[string]string{"address": "192.0.2.1"}, connect.CodePermissionDenied, "the diagnostic ping needs the operator role"},
		"no address":            {pki.RoleReader, "route-get", map[string]string{"address": "example.org"}, connect.CodeInvalidArgument, `diagnostic route-get: address: "example.org" is no IPv4 or IPv6 address`},
		"a missing argument":    {pki.RoleReader, "route-get", nil, connect.CodeInvalidArgument, "diagnostic route-get: it needs address=<address>"},
		"an unknown parameter":  {pki.RoleReader, "route-get", map[string]string{"address": "192.0.2.1", "count": "1"}, connect.CodeInvalidArgument, "it has no parameter count; it takes address"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeDiagnose{}
			_, conn := diagnosticsNode(t, f, tc.role)
			_, _, _, err := runDiagnostic(conn, tc.diagnostic, tc.args)
			if connect.CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %v with %q", err, tc.code, tc.want)
			}
			if runs := f.recorded(); len(runs) != 0 {
				t.Errorf("ran %+v", runs)
			}
		})
	}
}

// Past 1 MiB of output, the stream holds the first 1 MiB and ends with output-limit, and the run
// is stopped.
func TestRunDiagnosticCutsTheOutput(t *testing.T) {
	f := &fakeDiagnose{stdout: strings.Repeat("x", diagnostics.MaxOutput-100), stderr: strings.Repeat("y", 64<<10)}
	_, conn := diagnosticsNode(t, f, pki.RoleReader)
	stdout, stderr, exit, err := runDiagnostic(conn, "route-get", map[string]string{"address": "192.0.2.1"})
	if err != nil || len(stdout)+len(stderr) != diagnostics.MaxOutput || exit.GetStopped() != diagnostics.StoppedOutputLimit {
		t.Errorf("route-get = %d and %d bytes, %v, %v", len(stdout), len(stderr), exit, err)
	}
	if f.stops() != 1 {
		t.Errorf("the run was stopped %d times, want once", f.stops())
	}

	// Output of exactly the limit is not cut.
	f = &fakeDiagnose{stdout: strings.Repeat("x", diagnostics.MaxOutput), exit: diagnostics.Exit{Code: 0}}
	_, conn = diagnosticsNode(t, f, pki.RoleReader)
	stdout, _, exit, err = runDiagnostic(conn, "route-get", map[string]string{"address": "192.0.2.1"})
	if err != nil || len(stdout) != diagnostics.MaxOutput || exit == nil || exit.Stopped != "" || f.stops() != 0 {
		t.Errorf("route-get = %d bytes, %v, %v, stopped %d times", len(stdout), exit, err, f.stops())
	}
}

// A node runs four diagnostics at a time, and a runner that fails is an internal error.
func TestRunDiagnosticLimitsAndFailures(t *testing.T) {
	s, conn := diagnosticsNode(t, &fakeDiagnose{err: errors.New("no bus")}, pki.RoleReader)
	if _, _, _, err := runDiagnostic(conn, "route-get", map[string]string{"address": "192.0.2.1"}); connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), "run the diagnostic route-get: no bus") {
		t.Errorf("err = %v", err)
	}
	if n := s.diagnosticRuns.Load(); n != 0 {
		t.Errorf("%d runs counted after the run ended", n)
	}
	s.diagnosticRuns.Store(maxDiagnosticRuns)
	if _, _, _, err := runDiagnostic(conn, "route-get", map[string]string{"address": "192.0.2.1"}); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("err = %v, want resource exhausted", err)
	}
}
